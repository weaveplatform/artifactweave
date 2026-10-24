package docker

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestAccRemoteListingFinalFenceAndHead(t *testing.T) {
	for _, kind := range []Kind{KindTags, KindReferrers} {
		for _, mode := range []string{"success", "HEAD", "revoke earlier path", "revoke request"} {
			t.Run(kind.String()+"/"+mode, func(t *testing.T) {
				body := `{"name":"image","tags":["a","b"]}`
				if kind == KindReferrers {
					body = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:` + strings.Repeat(
						"a",
						64,
					) + `","size":1,"mediaType":"application/vnd.oci.image.manifest.v1+json"},{"digest":"sha256:` + strings.Repeat(
						"b",
						64,
					) + `","size":1,"mediaType":"application/vnd.oci.image.manifest.v1+json"}]}`
				}
				method := "GET"
				if mode == "HEAD" {
					method = "HEAD"
				}
				r := httptest.NewRequest(method, "/v2/remote/image/tags/list", nil)
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Name: "reader"}))
				w := httptest.NewRecorder()
				checks := map[string]int{}
				first := ""
				revoked, used, revalidated := false, false, false
				result := source.Result{
					Revalidate: func(context.Context) error {
						revalidated = true
						if mode == "revoke request" {
							return source.ErrDenied
						}
						return nil
					},
					CheckRead: func(_ context.Context, path string) error {
						if w.Header().Get("Content-Type") != "" || w.Header().Get("Link") != "" {
							t.Fatal("listing headers published before final fence")
						}
						if first == "" {
							first = path
						}
						checks[path]++
						if path != first && mode == "revoke earlier path" {
							revoked = true
						}
						if path == first && revoked {
							return source.ErrDenied
						}
						return nil
					},
					Used: func(context.Context) error { used = true; return nil },
				}
				g := &Registry{}
				g.remoteListing(
					w,
					r,
					Request{Kind: kind, RepoKey: "remote", Name: "image"},
					result,
					&source.Stream{Body: io.NopCloser(strings.NewReader(body))},
				)
				if strings.HasPrefix(mode, "revoke") {
					if w.Code == 200 || used || strings.Contains(w.Body.String(), `"tags"`) ||
						strings.Contains(w.Body.String(), `"manifests"`) {
						t.Fatalf("revoked listing delivered: %d %s", w.Code, w.Body)
					}
					return
				}
				if w.Code != 200 || !revalidated || len(checks) != 2 {
					t.Fatalf("successful listing: %d %v", w.Code, checks)
				}
				for _, count := range checks {
					if count != 2 {
						t.Fatal("retained path not rechecked", checks)
					}
				}
				if mode == "HEAD" {
					if w.Body.Len() != 0 || used || w.Header().Get("Content-Length") == "" {
						t.Fatal("HEAD served content or recorded use")
					}
				} else if w.Body.Len() == 0 || !used {
					t.Fatal("GET omitted body or usage")
				}
			})
		}
	}
}
