package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

// A tags listing joined from two pages carries Vary: *, which keeps it out
// of the remote cache, when its second page varies by request. A page
// marked Cache-Control: no-store no longer does: the cache keeps such an
// answer, as 7.x's remote does.
func TestAccRegistryJoinedListingMarksOnlyAVaryingPage(t *testing.T) {
	for _, tc := range []struct {
		name, header, value, vary string
	}{
		{"varying page", "Vary", "Cookie", "*"},
		{"no-store page", "Cache-Control", "no-store", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const path = "/v2/team/image/tags/list"
			router := chi.NewRouter()
			router.Get(path, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				tag := "a"
				if r.URL.RawQuery == "" {
					w.Header().Set("Link", "<"+path+"?last=a>; rel=\"next\"")
				} else {
					tag = "b"
					w.Header().Set(tc.header, tc.value)
				}
				_ = json.NewEncoder(w).
					Encode(map[string]any{"name": "team/image", "tags": []string{tag}})
			})
			up := httptest.NewServer(router)
			t.Cleanup(up.Close)
			client, err := upstream.NewRegistry(
				repo.Config{URL: up.URL, Class: repo.Remote, PackageType: repo.Docker},
				upstream.RegistryOptions{Transport: remote.Options{
					Revision: "test",
					Prepare:  func(*http.Request) error { return nil },
					CheckURL: func(context.Context, *url.URL) error { return nil },
				}},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			res, err := client.Do(
				t.Context(),
				"team/image",
				remote.Request{Method: "GET", Path: "tags/list"},
			)
			if err != nil {
				t.Fatal(err)
			}
			content := RegistryContent{Policy: &remote.Policy{}}
			res, err = content.pages(t.Context(), client, "team/image", "tags", "list", res)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.Header.Get("Vary") != tc.vary || res.Header.Get("Cache-Control") != "" {
				t.Fatalf("joined listing headers: %v", res.Header)
			}
		})
	}
}
