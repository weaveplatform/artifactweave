package remote

import (
	"context"
	"encoding/json"
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

func moduleLocationRequest(w *policyWorld) source.Request {
	q := w.request("module/download", source.ResourceKindMetadata)
	q.Resource.ModuleDownloadLocation = true
	q.Resource.Variant = "module-location-test-v1"
	q.Resource.ExpectedContentType = "application/json"
	q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, m source.MetadataResponse) error {
		var doc map[string]string
		if err := json.NewDecoder(r).
			Decode(&doc); err != nil || len(doc) != 1 || doc["source"] != "../package.tar.gz//*?archive=tar.gz&token=secret" ||
			m.BaseURL != w.up.URL+"/effective/download" {
			return source.ErrInvalidResult
		}
		return nil
	}
	return q
}

func TestAccModuleLocationProjectionCacheRefreshOfflineAndNoStore(t *testing.T) {
	for _, mode := range []string{"stored", "upstream-no-store", "no-local-store"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := moduleLocationRequest(w)
			if mode == "no-local-store" {
				q.Repo.StoreArtifactsLocally = ptr(false)
			}
			header := http.Header{
				"X-Terraform-Get":   {"../package.tar.gz//*?archive=tar.gz&token=secret"},
				"Content-Type":      {"text/html"},
				"X-Checksum-Sha256": {strings.Repeat("a", 64)},
			}
			if mode == "upstream-no-store" {
				header.Set("Cache-Control", "no-store")
			}
			w.up.Set(
				"/module/download",
				fakeupstream.Resource{Redirect: "/effective/download?private=query"},
			)
			w.up.Set(
				"/effective/download",
				fakeupstream.Resource{Status: 204, Header: header, ETag: `"one"`},
			)
			raw := `{"source":"../package.tar.gz//*?archive=tar.gz\u0026token=secret"}`
			result := w.mustRead(q, raw)
			if result.Metadata.BaseURL != w.up.URL+"/effective/download" ||
				result.Stream.MediaType != "application/json" ||
				result.Stream.ETag != "" {
				t.Fatal("projection metadata", result)
			}
			entries, _, err := w.db.RemoteCache().List(t.Context(), q.Repo.Key, "", store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "stored" {
				for _, e := range entries {
					if e.Blob.Checksums.SHA256 != "" {
						t.Fatal("transient projection persisted")
					}
				}
				return
			}
			if len(entries) != 1 || entries[0].MetadataBaseURL != w.up.URL+"/effective/download" ||
				entries[0].ETag != "" ||
				entries[0].OriginalChecksums != (types.Checksums{}) {
				t.Fatal("invalid cache projection", entries)
			}
			calls := len(w.up.Requests())
			w.mustRead(q, raw)
			if len(w.up.Requests()) != calls {
				t.Fatal("fresh cache fetched")
			}
			w.clock.Advance(time.Minute)
			w.mustRead(q, raw)
			for _, r := range w.up.Requests() {
				if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
					t.Fatal("conditional header-only request")
				}
			}
			q.Repo.Offline = ptr(true)
			w.up.Close()
			w.mustRead(q, raw)
		})
	}
}

func TestAccModuleLocationRejectRetryAndPublicationFence(t *testing.T) {
	for _, mode := range []string{"stored", "no-store"} {
		w := policyFixture(t)
		q := moduleLocationRequest(w)
		if mode == "no-store" {
			q.Repo.StoreArtifactsLocally = ptr(false)
		}
		w.up.Set("/module/download", fakeupstream.Resource{Redirect: "/effective/download"})
		for _, res := range []fakeupstream.Resource{{Status: 204}, {Status: 204, Header: http.Header{"X-Terraform-Get": {"one", "two"}}}, {Status: 204, Header: http.Header{"X-Terraform-Get": {"wrong"}}}, {Status: 200, Body: []byte(`{"source":"wrong"}`)}, {Status: 304}} {
			w.up.Set("/effective/download", res)
			if _, err := w.read(q); err == nil {
				t.Fatal("malformed control response admitted", res)
			}
		}
		w.up.Set(
			"/effective/download",
			fakeupstream.Resource{
				Status: 204,
				Header: http.Header{
					"X-Terraform-Get": {"../package.tar.gz//*?archive=tar.gz&token=secret"},
				},
			},
		)
		w.mustRead(q, `{"source":"../package.tar.gz//*?archive=tar.gz\u0026token=secret"}`)
	}
	w := policyFixture(t)
	q := moduleLocationRequest(w)
	w.up.Set("/module/download", fakeupstream.Resource{Redirect: "/effective/download"})
	w.up.Set(
		"/effective/download",
		fakeupstream.Resource{
			Status: 204,
			Header: http.Header{
				"X-Terraform-Get": {"../package.tar.gz//*?archive=tar.gz&token=secret"},
			},
		},
	)
	validate := q.Resource.ValidateMetadata
	q.Resource.ValidateMetadata = func(ctx context.Context, r io.ReadSeeker, n int64, m source.MetadataResponse) error {
		if err := validate(ctx, r, n, m); err != nil {
			return err
		}
		_, err := w.db.RemoteCache().Invalidate(ctx, q.Repo.Key, "", true)
		return err
	}
	if _, err := w.read(q); !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("zap did not fence projection", err)
	}
}

func TestAccModuleLocationProjectionBoundsAndIdentity(t *testing.T) {
	w := policyFixture(t)
	q := moduleLocationRequest(w)
	key := w.p.cacheKey(q, "revision")
	q.Resource.ModuleDownloadLocation = false
	if w.p.cacheKey(q, "revision") == key {
		t.Fatal("projection not part of identity")
	}
	q.Resource.ModuleDownloadLocation = true
	for _, value := range []string{"", strings.Repeat("x", 8193), " bad", "bad ", "bad\nheader", string([]byte{255})} {
		res := &http.Response{
			StatusCode: 204,
			Body:       http.NoBody,
			Header:     http.Header{"X-Terraform-Get": {value}},
		}
		if projectModuleLocation(q, res) == nil {
			t.Fatal("invalid header accepted")
		}
	}
	for _, res := range []*http.Response{
		{StatusCode: 204, ContentLength: 1, Body: http.NoBody},
		{StatusCode: 204, TransferEncoding: []string{"chunked"}, Body: http.NoBody},
	} {
		if projectModuleLocation(q, res) == nil {
			t.Fatal("nonempty204 accepted")
		}
	}
	res := &http.Response{StatusCode: 404, Body: http.NoBody}
	if projectModuleLocation(q, res) != nil || res.StatusCode != 404 {
		t.Fatal("error response changed")
	}
	q.Resource.ModuleDownloadLocation = false
	res.StatusCode = 204
	if projectModuleLocation(q, res) != nil || res.StatusCode != 204 {
		t.Fatal("generic behavior changed")
	}
	q.Resource.ModuleDownloadLocation = true
	req := transportRequest(
		q,
		types.RemoteCacheEntry{
			Blob:         types.Blob{Checksums: types.Checksums{SHA256: "digest"}},
			ETag:         `"old"`,
			LastModified: time.Now().Format(http.TimeFormat),
		},
	)
	if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
		t.Fatal("projection sent validators")
	}
}
