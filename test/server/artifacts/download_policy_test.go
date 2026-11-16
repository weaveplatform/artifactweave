package artifacts_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// patchRepo changes a repository's configuration in place, as an
// administrator does after its content was deployed.
func (w *itemWorld) patchRepo(key string, patch func(*repo.Config)) {
	w.t.Helper()
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	patch(&c)
	if err := w.db.Repos().Update(ctx, c); err != nil {
		w.t.Fatal(err)
	}
}

// wantRefusal checks a 404 whose body carries msg.
func wantRefusal(t *testing.T, w *itemWorld, method, path, as, msg string) {
	t.Helper()
	rec := w.do(method, path, as, "")
	wantStatus(t, rec, http.StatusNotFound)
	if method != http.MethodHead && !strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("%s %s: body %s, want %q", method, path, rec.Body, msg)
	}
}

// A download from a blacked-out repository answers 404, whether the item
// exists or not and for an administrator too; the storage API still
// describes its items, and only its file list is refused.
//
// A file request's refusal names the path it asked for after the reason,
// as 7.161.26's DownloadServiceImpl.respondResourceNotFound does for every
// resource it answers unfound, the one createUnfoundRepoResource makes of
// a refusal among them (#1630): a checksum's and an archive entry's path is
// the file it names, as for the check. A folder is answered by 7.x's
// directory listing (RepoFilter.doDirectoryRequest), which adds no path.
func TestAccDownloadFromBlackedOutRepository(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do("PUT", "/artifactory/other-local/a/b.jar", "admin", "b"), http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/other-local/z/a.zip", "admin",
		string(zipBytes(t, map[string]string{"f.txt": "f"}))), http.StatusCreated)
	w.patchRepo("other-local", func(c *repo.Config) { c.BlackedOut = ptr(true) })

	refusal := func(artifact string) string {
		return "The repository 'other-local' is blacked out and cannot serve artifact " +
			"'other-local:" + artifact + "'."
	}
	file := func(artifact string) string {
		return refusal(artifact) + "; Path: 'other-local:" + artifact + "'"
	}
	for _, c := range []struct{ method, path, as, message string }{
		{"GET", "/artifactory/other-local/a/b.jar", "admin", file("a/b.jar")},
		{"HEAD", "/artifactory/other-local/a/b.jar", "admin", ""},
		{"GET", "/artifactory/other-local/a/b.jar", "reader", file("a/b.jar")},
		{"GET", "/artifactory/other-local/a/b.jar.sha256", "admin", file("a/b.jar")},
		{"GET", "/artifactory/other-local/a/b.jar.sha1", "admin", file("a/b.jar")},
		{"HEAD", "/artifactory/other-local/a/b.jar.sha1", "admin", ""},
		{"GET", "/artifactory/other-local/z/a.zip!/f.txt", "admin", file("z/a.zip")},
		{"HEAD", "/artifactory/other-local/z/a.zip!/f.txt", "admin", ""},
		{"GET", "/artifactory/other-local/a/missing.jar", "admin", file("a/missing.jar")},
		{"GET", "/artifactory/other-local/a/", "admin", refusal("a")},
		{"GET", "/artifactory/other-local/", "admin", refusal("")},
	} {
		wantAnswer(t, c.method, w.do(c.method, c.path, c.as, ""), http.StatusNotFound,
			errorBody(t, http.StatusNotFound, c.message))
	}
	// A caller who may not read the path is answered as before, without
	// learning how the repository is set (DV-057).
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/b.jar", "user", ""),
		http.StatusForbidden)
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/b.jar", "", ""),
		http.StatusUnauthorized)

	// Artifactory 6.20's item info checks read permission alone.
	wantStatus(t, w.do("GET", "/artifactory/api/storage/other-local/a/b.jar", "admin", ""),
		http.StatusOK)
	wantRefusal(t, w, "GET", "/artifactory/api/storage/other-local/a?list", "admin",
		refusal("a"))

	w.patchRepo("other-local", func(c *repo.Config) { c.BlackedOut = ptr(false) })
	wantStatus(t, w.do("GET", "/artifactory/other-local/a/b.jar", "admin", ""), http.StatusOK)
}

// A path the repository's patterns reject answers 404 when it is
// downloaded, checked as Artifactory checks it: a sidecar as its file, an
// archive entry as its archive, a folder by its trailing slash. A file
// request's refusal names the file after the reason, as a blacked-out
// repository's does (#1630). Listings leave such paths out.
func TestAccDownloadOfExcludedPaths(t *testing.T) {
	w := newItemWorld(t, false)
	for p, body := range map[string]string{
		"a/x.jar": "x", "a/x.tmp": "t", "hidden/z.zip": string(zipBytes(t, map[string]string{
			"f.txt": "f",
		})),
	} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", body), http.StatusCreated)
	}
	excludes := "**/*.tmp, hidden/**"
	w.patchRepo("libs-local", func(c *repo.Config) { c.ExcludesPattern = &excludes })

	refusal := func(artifact string) string {
		return "The repository 'libs-local' rejected the artifact 'libs-local:" + artifact +
			"' due to its include/exclude pattern settings."
	}
	file := func(artifact string) string {
		return refusal(artifact) + "; Path: 'libs-local:" + artifact + "'"
	}
	for _, c := range []struct{ method, path, message string }{
		{"GET", "/artifactory/libs-local/a/x.tmp", file("a/x.tmp")},
		{"HEAD", "/artifactory/libs-local/a/x.tmp", ""},
		{"GET", "/artifactory/libs-local/a/x.tmp.sha1", file("a/x.tmp")},
		{"HEAD", "/artifactory/libs-local/a/x.tmp.sha1", ""},
		{"GET", "/artifactory/libs-local/a/never.tmp", file("a/never.tmp")},
		{"GET", "/artifactory/libs-local/hidden/z.zip!/f.txt", file("hidden/z.zip")},
		{"HEAD", "/artifactory/libs-local/hidden/z.zip!/f.txt", ""},
		{"GET", "/artifactory/libs-local/hidden/", refusal("hidden")},
	} {
		wantAnswer(t, c.method, w.do(c.method, c.path, "admin", ""), http.StatusNotFound,
			errorBody(t, http.StatusNotFound, c.message))
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/a/x.jar", "deployer", ""), http.StatusOK)
	// The SHA-256 file, which every deploy has: the client sent no SHA-1.
	wantStatus(t, w.do("GET", "/artifactory/libs-local/a/x.jar.sha256", "deployer", ""),
		http.StatusOK)

	for _, c := range []struct{ path, shown, left string }{
		{"/artifactory/libs-local/a/", `"x.jar"`, "x.tmp"},
		{"/artifactory/libs-local/", `"a/"`, "hidden"},
	} {
		rec := w.do("GET", c.path, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		if body := rec.Body.String(); !strings.Contains(body, c.shown) ||
			strings.Contains(body, c.left) {
			t.Fatalf("%s lists %s", c.path, body)
		}
	}
	// The storage API describes the path as given.
	wantStatus(t, w.do("GET", "/artifactory/api/storage/libs-local/a/x.tmp", "admin", ""),
		http.StatusOK)
}

// A virtual repository passes over a member that refuses a download, as it
// passes over one that lacks the item, and its own patterns leave it no
// member to search.
func TestAccVirtualDownloadPolicy(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do("PUT", "/artifactory/other-local/same.jar", "admin", "other"),
		http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/same.jar", "admin", "libs"),
		http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/other-local/only.jar", "admin", "only"),
		http.StatusCreated)
	body := func(path string) string {
		t.Helper()
		rec := w.do("GET", path, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		return rec.Body.String()
	}
	if got := body("/artifactory/virt/same.jar"); got != "other" {
		t.Fatalf("before %q", got)
	}

	w.patchRepo("other-local", func(c *repo.Config) { c.BlackedOut = ptr(true) })
	if got := body("/artifactory/virt/same.jar"); got != "libs" {
		t.Fatalf("blacked-out member %q", got)
	}
	wantRefusal(t, w, "GET", "/artifactory/virt/only.jar", "admin",
		"Could not find resource; Path: 'virt:only.jar'")
	w.patchRepo("other-local", func(c *repo.Config) { c.BlackedOut = ptr(false) })

	w.patchRepo("other-local", func(c *repo.Config) { c.ExcludesPattern = ptr("same.jar") })
	if got := body("/artifactory/virt/same.jar"); got != "libs" {
		t.Fatalf("excluding member %q", got)
	}
	w.patchRepo("other-local", func(c *repo.Config) { c.ExcludesPattern = ptr("") })

	// A virtual repository's blackedOut means nothing: Artifactory has none.
	w.patchRepo("virt", func(c *repo.Config) {
		c.BlackedOut = ptr(true)
		c.ExcludesPattern = ptr("**/*.secret")
	})
	if got := body("/artifactory/virt/only.jar"); got != "only" {
		t.Fatalf("blacked-out virtual %q", got)
	}
	wantRefusal(t, w, "GET", "/artifactory/virt/x.secret", "admin",
		"No repository found to serve the request for virt:x.secret; Path: 'virt:x.secret'")

	// A folder found in a member lists what both sets of patterns accept.
	for _, p := range []string{"dir/a.secret", "dir/b.jar"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", p), http.StatusCreated)
	}
	if got := body("/artifactory/virt/dir/"); !strings.Contains(got, `"b.jar"`) ||
		strings.Contains(got, "a.secret") {
		t.Fatalf("virtual listing %s", got)
	}
}
