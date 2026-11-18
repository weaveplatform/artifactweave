package docker_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func listingRemote(w *remoteWorld, path string, names []string, catalog bool) {
	value := map[string]any{"name": "team/image", "tags": names}
	if catalog {
		value = map[string]any{"repositories": names}
	}
	w.up.Set(path, fakeupstream.Resource{Body: registryJSON(w.t, value), Header: http.Header{"Content-Type": {"application/json"}}})
}

func listingNames(t *testing.T, w *remoteWorld, target, user string, catalog bool) ([]string, string) {
	t.Helper()
	r := w.request("GET", target, user, "")
	remoteStatus(t, r, 200)
	var doc struct {
		Name         string   `json:"name"`
		Tags         []string `json:"tags"`
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
		t.Fatal(err, r.Body.String())
	}
	names := doc.Tags
	if catalog {
		names = doc.Repositories
	}
	if names == nil {
		t.Fatal("null list", r.Body.String())
	}
	return names, r.Header().Get("Link")
}

func listingLocal(t *testing.T, w *remoteWorld, key, image, tag string) {
	t.Helper()
	manifest, _ := registryImage(t, w, "listing")
	remoteStatus(t, w.request("PUT", "/artifactory/"+key+"/"+image+"/"+tag+"/manifest.json", "admin", string(manifest), "Content-Type", layout.MediaTypeOCIManifest), 201)
}

func TestAccRegistryVirtualListingPages(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Docker, repo.OCI, repo.HelmOCI} {
		t.Run(string(kind), func(t *testing.T) {
			w := virtualRegistryWorld(t, kind)
			for _, tag := range []string{"A", "a", "b", "same", "z"} {
				listingLocal(t, w, "first", "team/image", tag)
			}
			listingRemote(w, "/v2/team/image/tags/list", []string{"same", "c", "A", "Z", "d", "d", "sha256:" + strings.Repeat("a", 64)}, false)
			for _, name := range []string{"busybox", "ctest", "local", "same"} {
				listingLocal(t, w, "first", name, "v1")
			}
			listingRemote(w, "/v2/_catalog", []string{"same", "ctest", "remote", "atest"}, true)
			for _, tc := range []struct {
				path    string
				catalog bool
				all     []string
			}{{"/v2/virtual/team/image/tags/list", false, []string{"A", "a", "b", "c", "d", "same", "Z", "z"}}, {"/artifactory/api/docker/virtual/v2/_catalog", true, []string{"atest", "busybox", "ctest", "local", "remote", "same", "team/image"}}} {
				for _, last := range []string{"", "A", "a", "b", "same", "zzzz", "~"} {
					for _, n := range []int{0, 1, 2, 5, 20} {
						target := tc.path + "?" + url.Values{"n": {fmt.Sprint(n)}, "last": {last}}.Encode()
						got, link := listingNames(t, w, target, "admin", tc.catalog)
						all := slices.DeleteFunc(slices.Clone(tc.all), func(name string) bool { return last != "" && layout.Compare(name, last) <= 0 })
						want := all[:min(n, len(all))]
						if !reflect.DeepEqual(got, want) {
							t.Fatal(target, got, want)
						}
						if n > 0 && len(all) > n {
							if !strings.Contains(link, "last="+url.QueryEscape(want[len(want)-1])) || !strings.Contains(link, tc.path+"?") {
								t.Fatal(target, link)
							}
						} else if link != "" {
							t.Fatal(target, link)
						}
					}
				}
			}
			names, _ := listingNames(t, w, "/v2/_catalog?n=100", "admin", true)
			if !slices.Contains(names, "virtual/remote") || slices.Contains(names, "remote/remote") {
				t.Fatal(names)
			}
			// Registry-wide prefix seeking must not repeat earlier repository names.
			names, _ = listingNames(t, w, "/v2/_catalog?n=100&last=virtual%2Fctest", "admin", true)
			if !slices.Contains(names, "virtual/local") || slices.Contains(names, "virtual/ctest") {
				t.Fatal(names)
			}
		})
	}
}

func TestAccRegistryVirtualCatalogVisibilityAndPatterns(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	for _, name := range []string{"busybox", "ctest", "private", "team/image"} {
		listingLocal(t, w, "first", name, "v1")
	}
	listingRemote(w, "/v2/_catalog", []string{"busybox", "ctest", "remote-private"}, true)
	if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "scoped", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "scoped-read", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"scoped": accessapi.ActionRead | accessapi.ActionWrite}}, Targets: map[string]accesstest.Target{"remote": {Include: []string{"team/image/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "scoped-cache-fill", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"scoped": accessapi.ActionWrite}}, Targets: map[string]accesstest.Target{"remote": {}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"remote", "virtual"} {
		names, _ := listingNames(t, w, "/artifactory/api/docker/"+key+"/v2/_catalog", "scoped", true)
		if !reflect.DeepEqual(names, []string{"busybox", "ctest", "remote-private"}) {
			t.Fatal(key, names)
		}
	}
	names, _ := listingNames(t, w, "/v2/virtual/_catalog", "denied", true)
	if len(names) != 0 {
		t.Fatal(names)
	}
	virtualRegistryConfig(t, w, "virtual", func(c *repo.Config) { c.IncludesPattern = new("b*/**,c*/**") })
	names, _ = listingNames(t, w, "/v2/virtual/_catalog?n=2", "admin", true)
	if !reflect.DeepEqual(names, []string{"busybox", "ctest"}) {
		t.Fatal(names)
	}
	virtualRegistryConfig(t, w, "first", func(c *repo.Config) { c.ExcludesPattern = new("c*/**") })
	virtualRegistryConfig(t, w, "remote", func(c *repo.Config) { c.BlackedOut = new(true) })
	names, _ = listingNames(t, w, "/v2/virtual/_catalog?n=1", "admin", true)
	if !reflect.DeepEqual(names, []string{"busybox"}) {
		t.Fatal(names)
	}
}

func TestAccRegistryVirtualListingFailureAndProjects(t *testing.T) {
	for _, status := range []int{401, 403, 404, 502, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			w := virtualRegistryWorld(t, repo.Docker)
			listingLocal(t, w, "first", "team/image", "local")
			w.up.Set("/v2/team/image/tags/list", fakeupstream.Resource{Status: status})
			w.up.Set("/v2/_catalog", fakeupstream.Resource{Status: status})
			names, _ := listingNames(t, w, "/v2/virtual/team/image/tags/list", "admin", false)
			if !reflect.DeepEqual(names, []string{"local"}) {
				t.Fatal(names)
			}
			names, _ = listingNames(t, w, "/v2/virtual/_catalog", "admin", true)
			if !reflect.DeepEqual(names, []string{"team/image"}) {
				t.Fatal(names)
			}
			remoteStatus(t, w.request("GET", "/v2/virtual/absent/tags/list", "admin", ""), 404)
		})
	}
	w := virtualRegistryWorld(t, repo.Docker)
	virtualRegistryConfig(t, w, "remote", func(c *repo.Config) { c.DockerProjectID = "project" })
	listingRemote(w, "/v2/_catalog", []string{"other/private", "project/a", "project/team/image", "project2/wrong"}, true)
	names, _ := listingNames(t, w, "/v2/remote/_catalog?last=a&n=1", "admin", true)
	if !reflect.DeepEqual(names, []string{"team/image"}) {
		t.Fatal(names)
	}
	names, _ = listingNames(t, w, "/v2/virtual/_catalog?last=a&n=1", "admin", true)
	if !reflect.DeepEqual(names, []string{"team/image"}) {
		t.Fatal(names)
	}
	if w.up.Count("GET", "/v2/_catalog") != 1 {
		t.Fatal("catalog was not cached", w.up.Requests())
	}
}

func TestAccRegistryWarmCatalogNeedsOnlyScopedRead(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	listingRemote(w, "/v2/_catalog", []string{"private", "team/image"}, true)
	listingNames(t, w, "/v2/remote/_catalog", "admin", true)
	if err := w.rt.Store.Users().Create(t.Context(), accesstest.User{Name: "warm", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "warm-read", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"warm": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"remote": {Include: []string{"team/image/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"remote", "virtual"} {
		names, _ := listingNames(t, w, "/v2/"+key+"/_catalog", "warm", true)
		if !reflect.DeepEqual(names, []string{"private", "team/image"}) {
			t.Fatal(names)
		}
	}
	if w.up.Count("GET", "/v2/_catalog") != 1 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccRegistryCatalogDoesNotFallBackToCachedImages(t *testing.T) {
	w := virtualRegistryWorld(t, repo.Docker)
	manifest, _ := registryImage(t, w, "cached")
	registryPut(w, "manifests", "cached", layout.MediaTypeOCIManifest, manifest)
	remoteStatus(t, w.request("GET", "/v2/remote/team/image/manifests/cached", "admin", ""), 200)
	w.up.Set("/v2/_catalog", fakeupstream.Resource{Status: 502})
	remoteStatus(t, w.request("GET", "/v2/remote/_catalog", "admin", ""), 502)
	names, _ := listingNames(t, w, "/v2/virtual/_catalog", "admin", true)
	if len(names) != 0 {
		t.Fatal("fell back to stored cached manifests", names)
	}
}

func TestAccRegistryMalformedRemoteCatalogs(t *testing.T) {
	for i, body := range []string{`{`, `{}`, `{"repositories":null}`, `{"repositories":"bad"}`, `{"repositories":[1]}`} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			w := virtualRegistryWorld(t, repo.Docker)
			w.up.Set("/v2/_catalog", fakeupstream.Resource{Body: []byte(body), Header: http.Header{"Content-Type": {"application/json"}}})
			remoteStatus(t, w.request("GET", "/v2/remote/_catalog", "admin", ""), 502)
			names, _ := listingNames(t, w, "/v2/virtual/_catalog", "admin", true)
			if len(names) != 0 {
				t.Fatal(names)
			}
		})
	}
}
