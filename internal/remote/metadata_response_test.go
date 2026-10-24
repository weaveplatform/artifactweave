package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccMetadataResponseBaseAdmissionCacheOfflineAndNoStore(t *testing.T) {
	for _, mode := range []string{"stored", "no-local-store", "upstream-no-store"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set(
				"/index.json",
				fakeupstream.Resource{Redirect: "/redirected/index.json?secret=redacted#fragment"},
			)
			header := http.Header{"Content-Type": {"application/json"}}
			if mode == "upstream-no-store" {
				header.Set("Cache-Control", "no-store")
			}
			w.up.Set(
				"/redirected/index.json",
				fakeupstream.Resource{
					Body:   []byte(`{"links":["file.whl"]}`),
					Header: header,
					ETag:   `"one"`,
				},
			)
			q := w.request("index.json", source.Metadata)
			q.Resource.Variant = "response-base-v1"
			q.Resource.ExpectedContentTypes = []string{"application/json", "text/html"}
			if mode == "no-local-store" {
				q.Repo.StoreArtifactsLocally = ptr(false)
			}
			calls := 0
			want := w.up.URL + "/redirected/index.json"
			q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, n int64, m source.MetadataResponse) error {
				calls++
				b, err := io.ReadAll(r)
				if err != nil || string(b) != `{"links":["file.whl"]}` || n != int64(len(b)) ||
					m.BaseURL != want {
					t.Errorf("admission body/base: %q %d %+v %v", b, n, m, err)
				}
				return err
			}
			result := w.mustRead(q, `{"links":["file.whl"]}`)
			if calls == 0 || result.Metadata.BaseURL != want {
				t.Fatalf("missing admission/response context: %d %+v", calls, result.Metadata)
			}
			field, ok := reflect.TypeFor[source.Result]().FieldByName("Metadata")
			if !ok || field.Tag.Get("json") != "-" {
				t.Fatal("private context lacks JSON exclusion")
			}

			entries, _, err := w.db.RemoteCache().List(t.Context(), w.c.Key, "", store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "stored" {
				for _, entry := range entries {
					if entry.Blob.Checksums.SHA256 != "" {
						t.Fatal("no-store persisted body")
					}
				}
				return
			}
			if len(entries) != 1 || entries[0].MetadataBaseURL != want {
				t.Fatalf("base not persisted: %+v", entries)
			}
			oldCalls := calls
			result = w.mustRead(q, `{"links":["file.whl"]}`)
			if calls != oldCalls || result.Metadata.BaseURL != want {
				t.Fatal("cache hit lost response base")
			}
			q.Resource.Method = http.MethodHead
			result = w.mustRead(q, "")
			if result.Metadata.BaseURL != want {
				t.Fatal("HEAD lost response base")
			}
			q.Resource.Method = http.MethodGet
			q.Repo.Offline = ptr(true)
			w.up.Set("/redirected/index.json", fakeupstream.Resource{Status: 503})
			result = w.mustRead(q, `{"links":["file.whl"]}`)
			if result.Metadata.BaseURL != want || calls != oldCalls {
				t.Fatal("offline base drifted")
			}
		})
	}
}

func TestAccMetadataResponseBaseRevalidationRejectsInvalidNewBase(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/index.json", fakeupstream.Resource{Redirect: "/first/index.json"})
	w.up.Set(
		"/first/index.json",
		fakeupstream.Resource{
			Body:   []byte(`{"file":"file.whl"}`),
			ETag:   `"one"`,
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	q := w.request("index.json", source.Metadata)
	q.Resource.Variant = "response-base-v1"
	q.Resource.ExpectedContentType = "application/json"
	allowed := w.up.URL + "/first/index.json"
	calls := 0
	q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, m source.MetadataResponse) error {
		calls++
		b, err := io.ReadAll(r)
		if err != nil || string(b) != `{"file":"file.whl"}` {
			t.Fatalf("304 validation bytes: %q %v", b, err)
		}
		if m.BaseURL != allowed {
			return errors.New("untrusted response base")
		}
		return nil
	}
	w.mustRead(q, `{"file":"file.whl"}`)
	w.clock.Advance(time.Minute)
	w.up.Set("/index.json", fakeupstream.Resource{Redirect: "/second/index.json?secret=redacted"})
	w.up.Set("/second/index.json", fakeupstream.Resource{Status: 304, ETag: `"one"`})
	if _, err := w.read(q); err == nil {
		t.Fatal("304 base changed without valid admission")
	}
	entries, _, err := w.db.RemoteCache().List(t.Context(), w.c.Key, "", store.Page{})
	if err != nil || len(entries) != 1 || entries[0].MetadataBaseURL != allowed {
		t.Fatal("failed admission changed stored base", entries, err)
	}
	allowed = w.up.URL + "/second/index.json"
	result := w.mustRead(q, `{"file":"file.whl"}`)
	if result.Metadata.BaseURL != allowed || calls != 3 {
		t.Fatalf("corrected 304 failed immediate retry: %+v calls=%d", result.Metadata, calls)
	}
	entries, _, err = w.db.RemoteCache().List(t.Context(), w.c.Key, "", store.Page{})
	if err != nil || entries[0].MetadataBaseURL != allowed {
		t.Fatal("validated base not persisted", err)
	}
	q.Repo.Offline = ptr(true)
	result = w.mustRead(q, `{"file":"file.whl"}`)
	if result.Metadata.BaseURL != allowed {
		t.Fatal("offline retained stale base")
	}
}

func TestUnitResponseBaseSanitizationAndLegacyCompatibility(t *testing.T) {
	u, err := url.Parse("https://user:password@example.com/dir/index.json?secret=value#fragment")
	if err != nil {
		t.Fatal(err)
	}
	res := &http.Response{Request: &http.Request{URL: u}}
	if got := responseBase(res); got != "https://example.com/dir/index.json" {
		t.Fatal(got)
	}
	if metadataResponse(res, types.RemoteUnique).BaseURL != "" ||
		responseBase(&http.Response{}) != "" {
		t.Fatal("artifact or absent URL gained context")
	}
	w := policyFixture(t)
	w.up.Set("/index.json", fakeupstream.Resource{Body: []byte("legacy")})
	q := w.request("index.json", source.Metadata)
	w.mustRead(q, "legacy")
	entries, _, err := w.db.RemoteCache().List(t.Context(), w.c.Key, "", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	e.MetadataBaseURL = ""
	if _, err = w.db.RemoteCache().Put(t.Context(), e.Token, e); err != nil {
		t.Fatal(err)
	}
	q.Repo.Offline = ptr(true)
	result := w.mustRead(q, "legacy")
	if result.Metadata.BaseURL != "" {
		t.Fatal("invented base for legacy entry")
	}
}

func TestAccMetadataResponseAdmissionFailureRetryAndPublicationFence(t *testing.T) {
	for _, phase := range []string{"initial", "304"} {
		t.Run(phase, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set(
				"/index.json",
				fakeupstream.Resource{
					Body:   []byte(`{"valid":true}`),
					ETag:   `"one"`,
					Header: http.Header{"Content-Type": {"application/json"}},
				},
			)
			q := w.request("index.json", source.Metadata)
			q.Resource.Variant = "response-base-v1"
			q.Resource.ExpectedContentType = "application/json"
			reject := phase == "initial"
			mutate := false
			q.Resource.ValidateMetadata = func(ctx context.Context, _ io.ReadSeeker, _ int64, m source.MetadataResponse) error {
				if reject {
					return errors.New("validation leaked " + m.BaseURL + "?secret=credential")
				}
				if mutate {
					c, err := w.db.Repos().Get(ctx, w.c.Key)
					if err != nil {
						return err
					}
					c.URL = "https://changed.example"
					return w.db.Repos().Update(ctx, c)
				}
				return nil
			}
			if reject {
				if _, err := w.read(
					q,
				); err == nil || strings.Contains(err.Error(), w.up.URL) ||
					strings.Contains(err.Error(), "credential") {
					t.Fatalf("unsafe error: %v", err)
				}
				reject = false
			}
			w.mustRead(q, `{"valid":true}`)
			w.clock.Advance(time.Minute)
			mutate = true
			if _, err := w.read(q); !errors.Is(err, source.ErrSuperseded) {
				t.Fatalf("metadata publication ignored source change: %v", err)
			}
		})
	}
}
