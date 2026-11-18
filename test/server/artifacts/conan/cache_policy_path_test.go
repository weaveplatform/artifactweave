package conan_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func TestAccConanBlackoutOnlyCachePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, include string
	}{{"file-only", "**/conanfile.py"}, {"manifest-only", "**/conanmanifest.txt"}, {"unrestricted", "**"}} {
		include := tc.include
		for _, route := range []string{"", "/conanfile.py"} {
			name := "list"
			if route != "" {
				name = "file"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				w := newRemoteWorld(t)
				c := *repo.Defaults(&repo.Config{Key: "conan-up", Class: repo.Remote, PackageType: repo.Conan, URL: w.up.URL, IncludesPattern: new(include)})
				if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				const root = "/v2/conans/hello/1.0/_/_/revisions/abc"
				w.up.Set(
					root+"/files",
					fakeupstream.Resource{Body: []byte(`{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`), Header: http.Header{"Content-Type": {"application/json"}}},
				)
				w.up.Set(root+"/files/conanmanifest.txt", fakeupstream.Resource{Body: fmt.Appendf(nil, "1\nconanfile.py: %s\n", md5Hex("recipe"))})
				w.up.Set(root+"/files/conanfile.py", fakeupstream.Resource{Body: []byte("recipe")})
				r := w.request("GET", "/artifactory/api/conan/conan-up"+root+"/files"+route, "admin", "")
				want := 502
				if include == "**" || include == "**/conanmanifest.txt" && route == "" {
					want = 200
				}
				if include == "**/conanmanifest.txt" && route != "" {
					want = 404
				}
				if r.Code != want {
					t.Fatalf("include=%q route=%q: status%d want%d: %s", include, route, r.Code, want, r.Body.String())
				}
				if w.up.Count("GET", root+"/files/conanmanifest.txt") != 1 {
					t.Fatal("manifest lookup changed")
				}
				if want == 200 && route != "" && r.Body.String() != "recipe" {
					t.Fatal("file bytes changed")
				}
				if want == 200 && route == "" && !strings.Contains(r.Body.String(), "conanfile.py") {
					t.Fatal("file list changed", r.Body.String())
				}
			})
		}
	}
}
