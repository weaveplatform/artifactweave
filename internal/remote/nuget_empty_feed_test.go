package remote

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func nugetEmptyRequest(w *policyWorld) source.Request {
	q := w.request("v2/feed", source.Metadata)
	q.Resource.NuGetEmptyFeed = true
	q.Resource.Variant = "atom-feed-test-v1"
	q.Resource.ExpectedContentType = "application/atom+xml"
	q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, m source.MetadataResponse) error {
		var doc struct {
			XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		}
		if err := xml.NewDecoder(r).
			Decode(&doc); err != nil ||
			m.BaseURL != w.up.URL+"/redirected/feed" {
			return source.ErrInvalidResult
		}
		return nil
	}
	return q
}

func TestNuGetEmptyFeedCacheRefreshOfflineAndNoStore(t *testing.T) {
	for _, mode := range []string{"stored", "response-no-store", "repository-no-store"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := nugetEmptyRequest(w)
			if mode == "repository-no-store" {
				q.Repo.StoreArtifactsLocally = ptr(false)
			}
			h := http.Header{
				"Content-Type":      {"application/atom+xml"},
				"X-Checksum-Sha256": {strings.Repeat("a", 64)},
			}
			if mode == "response-no-store" {
				h.Set("Cache-Control", "no-store")
			}
			w.up.Set(
				"/v2/feed",
				fakeupstream.Resource{Redirect: "/redirected/feed?credential=secret"},
			)
			w.up.Set(
				"/redirected/feed",
				fakeupstream.Resource{Status: 204, Header: h, ETag: `"empty"`},
			)
			result := w.mustRead(q, emptyNuGetFeed)
			if result.Metadata.BaseURL != w.up.URL+"/redirected/feed" ||
				result.Stream.MediaType != "application/atom+xml" ||
				result.Stream.ETag != "" {
				t.Fatal("projection context lost", result)
			}
			entries, _, err := w.db.RemoteCache().List(t.Context(), q.Repo.Key, "", store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "stored" {
				for _, e := range entries {
					if e.Blob.Checksums.SHA256 != "" {
						t.Fatal("transient feed persisted")
					}
				}
				return
			}
			if len(entries) != 1 || entries[0].ETag != "" ||
				entries[0].OriginalChecksums != (types.Checksums{}) {
				t.Fatal("204 validators retained", entries)
			}
			calls := len(w.up.Requests())
			w.mustRead(q, emptyNuGetFeed)
			if len(w.up.Requests()) != calls {
				t.Fatal("fresh metadata refetched")
			}
			w.clock.Advance(time.Minute)
			w.mustRead(q, emptyNuGetFeed)
			for _, r := range w.up.Requests() {
				if r.Header.Get("If-None-Match") != "" {
					t.Fatal("synthetic validator forwarded")
				}
			}
			q.Repo.Offline = ptr(true)
			w.up.Close()
			w.mustRead(q, emptyNuGetFeed)
		})
	}
}

func TestNuGetEmptyFeedValidationAndPublicationFence(t *testing.T) {
	for _, persist := range []bool{true, false} {
		w := policyFixture(t)
		q := nugetEmptyRequest(w)
		q.Repo.StoreArtifactsLocally = &persist
		w.up.Set("/v2/feed", fakeupstream.Resource{Redirect: "/redirected/feed"})
		for _, res := range []fakeupstream.Resource{
			{Status: 204, Header: http.Header{"Content-Type": {"text/html"}}},
			{Status: 200, Header: http.Header{"Content-Type": {"application/atom+xml"}}, Body: []byte("<html/>")},
		} {
			w.up.Set("/redirected/feed", res)
			if _, err := w.read(q); err == nil {
				t.Fatal("invalid projection admitted")
			}
		}
		w.up.Set("/redirected/feed", fakeupstream.Resource{Status: 204})
		original := q.Resource.ValidateMetadata
		q.Resource.ValidateMetadata = func(ctx context.Context, r io.ReadSeeker, n int64, m source.MetadataResponse) error {
			if err := original(ctx, r, n, m); err != nil {
				return err
			}
			if !persist {
				c := q.Repo
				c.Password = "rotated-during-validation"
				return w.db.Repos().Update(ctx, c)
			}
			_, err := w.db.RemoteCache().Invalidate(ctx, q.Repo.Key, "", true)
			return err
		}
		if _, err := w.read(q); !errors.Is(err, source.ErrSuperseded) {
			t.Fatal("generation fence lost", err)
		}
	}
}

func TestNuGetEmptyFeedIdentityAndGenericStatus(t *testing.T) {
	w := policyFixture(t)
	q := nugetEmptyRequest(w)
	key := w.p.cacheKey(q, "revision")
	q.Resource.NuGetEmptyFeed = false
	if key == w.p.cacheKey(q, "revision") {
		t.Fatal("projection not included in identity")
	}
	w.up.Set("/v2/feed", fakeupstream.Resource{Status: 204})
	if _, err := w.read(q); err == nil {
		t.Fatal("generic204 changed")
	}
	q.Resource.NuGetEmptyFeed = true
	for _, res := range []*http.Response{
		{StatusCode: 204, ContentLength: 1, Body: http.NoBody},
		{StatusCode: 204, TransferEncoding: []string{"chunked"}, Body: http.NoBody},
		{StatusCode: 204, Header: http.Header{"Content-Encoding": {"gzip"}}, Body: http.NoBody},
		{StatusCode: 204, Header: http.Header{"Content-Type": {"malformed type"}}, Body: http.NoBody},
	} {
		if projectNuGetEmptyFeed(q, res) == nil {
			t.Fatal("deceptive204 accepted")
		}
	}
	for _, status := range []int{200, 304, 401, 404, 500} {
		res := &http.Response{StatusCode: status, Body: http.NoBody}
		if err := projectNuGetEmptyFeed(q, res); err != nil || res.StatusCode != status {
			t.Fatal("non204 changed", status, err)
		}
	}
}
