package artifacts_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// The member order of a virtual repository is 7.x's effective order
// (decision 0008, R1 and R2; #1252): local members before remote members'
// caches, priorityResolution first, nested virtual repositories flattened
// depth first, and a virtual reached twice skipped without an error.

// virtualOver adds the virtual repository key over members.
func (w *itemWorld) virtualOver(key string, members ...string) {
	w.t.Helper()
	if err := w.db.Repos().Create(w.t.Context(), *repo.Defaults(&repo.Config{
		Key: key, Class: repo.Virtual, Repositories: members,
	})); err != nil {
		w.t.Fatal(err)
	}
}

// wantBody checks a 200 whose body is want.
func wantBody(t *testing.T, w *itemWorld, p, want string) {
	t.Helper()
	rec := w.do(http.MethodGet, p, "admin", "")
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("GET %s: %d %q, want %q", p, rec.Code, rec.Body, want)
	}
}

func TestAccVirtualServesLocalBeforeRemoteCache(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/f.bin", "local")
	w.cacheFile("up", "f.bin", "cached")
	w.cacheFile("up", "only-cached.bin", "cached")
	w.virtualOver("remote-first", "up", "libs-local")
	w.virtualOver("local-first", "libs-local", "up")
	// A virtual listing the remote first serves the local copy, as 7.x
	// searches every local member before any remote cache.
	wantBody(t, w, "/artifactory/remote-first/f.bin", "local")
	wantBody(t, w, "/artifactory/remote-first/only-cached.bin", "cached")
	// HEAD takes the same member.
	rec := w.do(http.MethodHead, "/artifactory/remote-first/f.bin", "admin", "")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Checksum-Sha256") != sha256Hex("local") {
		t.Fatalf("HEAD %d %v", rec.Code, rec.Header())
	}
	// With priorityResolution on the remote, its cache comes before a
	// local member without it, whatever the configured order.
	w.patchRepo("up", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	wantBody(t, w, "/artifactory/local-first/f.bin", "cached")
	wantBody(t, w, "/artifactory/remote-first/f.bin", "cached")
	// A local member with priorityResolution comes first again.
	w.patchRepo("libs-local", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	wantBody(t, w, "/artifactory/remote-first/f.bin", "local")
}

func TestAccVirtualNestingAndCycles(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/both.txt", "libs")
	w.put("/artifactory/other-local/both.txt", "other")
	w.put("/artifactory/libs-local/libs.txt", "libs")
	// a -> [b, libs-local] and b -> [a, other-local]: a reaches itself
	// through b, which the walk skips, and resolves other-local, then
	// libs-local.
	w.virtualOver("a", "b", "libs-local")
	w.virtualOver("b", "a", "other-local")
	wantBody(t, w, "/artifactory/a/both.txt", "other")
	wantBody(t, w, "/artifactory/a/libs.txt", "libs")
	wantBody(t, w, "/artifactory/b/both.txt", "libs")
	// A virtual over itself alone has no member to search, which 7.x
	// answers as it answers its own patterns rejecting the path (#1533).
	wantRefusal(
		t,
		w,
		http.MethodGet,
		"/artifactory/loop/both.txt",
		"admin",
		"No repository found to serve the request for loop:both.txt; Path: 'loop:both.txt'",
	)
	// Nested members are grouped with the top virtual's own: a local member
	// of a nested virtual comes before a remote cache listed ahead of it.
	w.cacheFile("up", "both.txt", "cached")
	w.virtualOver("remotes", "up")
	w.virtualOver("nested", "remotes", "b")
	wantBody(t, w, "/artifactory/nested/both.txt", "libs")
}

func TestAccVirtualNestedPatterns(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/other-local/f.bin", "other")
	w.put("/artifactory/libs-local/f.bin", "libs")
	w.put("/artifactory/other-local/f.txt", "other")
	w.cacheFile("up", "f.bin", "cached")
	w.virtualOver("no-bin", "other-local")
	w.patchRepo("no-bin", func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.bin") })
	w.virtualOver("outer", "up", "no-bin", "libs-local")
	// The nested virtual whose excludes match the path is passed over with
	// its members; the others answer in 7.x's order.
	wantBody(t, w, "/artifactory/outer/f.bin", "libs")
	wantBody(t, w, "/artifactory/outer/f.txt", "other")
	// The top virtual's own pattern miss answers as before.
	wantRefusal(t, w, http.MethodGet, "/artifactory/no-bin/f.bin", "admin",
		"No repository found to serve the request for no-bin:f.bin")
}

func TestAccVirtualStorageInfoNamesLocalCopy(t *testing.T) {
	w := newItemWorld(t, false)
	w.put("/artifactory/libs-local/d/f.bin", "local")
	w.put("/artifactory/libs-local/d/local.txt", "local")
	w.cacheFile("up", "d/f.bin", "cached")
	w.cacheFile("up", "d/cached.txt", "cached")
	w.virtualOver("remote-first", "up", "libs-local")
	var info struct {
		Repo      string            `json:"repo"`
		Size      string            `json:"size"`
		Checksums map[string]string `json:"checksums"`
		Children  []struct {
			URI string `json:"uri"`
		} `json:"children"`
	}
	rec := w.do(http.MethodGet, "/artifactory/api/storage/remote-first/d/f.bin", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Repo != "remote-first" || info.Checksums["sha256"] != sha256Hex("local") {
		t.Fatalf("file info %s", rec.Body)
	}
	// A folder's children list the local member's names first, then the
	// names only the cache holds.
	rec = w.do(http.MethodGet, "/artifactory/api/storage/remote-first/d", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	var children []string
	for _, c := range info.Children {
		children = append(children, c.URI)
	}
	if want := []string{"/f.bin", "/local.txt", "/cached.txt"}; !slices.Equal(children, want) {
		t.Fatalf("children %q, want %q", children, want)
	}
}

func TestAccTransitiveAQLThroughNestedCycle(t *testing.T) {
	w := transitiveFixture(t)
	w.virtualOver("plain", "up", "libs-local")
	w.virtualOver("cycle", "cycle-inner", "up", "libs-local")
	w.virtualOver("cycle-inner", "cycle")
	w.virtualOver("gap", "missing", "up", "libs-local")
	want := hitNames(w.search(t, `items.find({"repo":"plain"}).transitive()`, "reader"))
	if !slices.Contains(want, "up/fresh.txt") {
		t.Fatal("plain virtual", want)
	}
	// A repeated virtual and a member key no repository has answer as if
	// they were absent.
	for _, key := range []string{"cycle", "gap"} {
		got := hitNames(w.search(t, `items.find({"repo":"`+key+`"}).transitive()`, "reader"))
		if !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	// A virtual over itself alone searches nothing.
	w.calls = nil
	if got := w.search(t, `items.find({"repo":"loop"}).transitive()`, "admin"); len(got) != 0 ||
		len(w.calls) != 0 {
		t.Fatal("loop", got, w.calls)
	}
}

func TestAccSearchUpstreamThroughNestedCycle(t *testing.T) {
	u := searchUpstreamWorld(t)
	u.metadata("org/acme/lib", mappedVersions)
	u.listing("org/acme/lib", "1.0/", "2.0/")
	u.listing("org/acme/lib/1.0", "lib-1.0.jar")
	u.listing("org/acme/lib/2.0", "lib-2.0.jar")
	searchVirtual(t, u, "plain", []string{"up"}, "", "")
	searchVirtual(t, u, "cycle", []string{"cycle-inner", "up"}, "", "")
	searchVirtual(t, u, "cycle-inner", []string{"cycle"}, "", "")
	for _, query := range []string{
		"versions?g=org.acme&a=lib&remote=1&repos=",
		"gavc?g=org.acme&a=lib&specific=true&remote=1&repos=",
	} {
		want := u.do(http.MethodGet, "/artifactory/api/search/"+query+"plain", "reader", "")
		wantStatus(t, want, http.StatusOK)
		got := u.do(http.MethodGet, "/artifactory/api/search/"+query+"cycle", "reader", "")
		wantStatus(t, got, http.StatusOK)
		if strings.ReplaceAll(want.Body.String(), "/plain/", "/cycle/") != got.Body.String() {
			t.Errorf("%s: %s, want as %s", query, got.Body, want.Body)
		}
	}
}
