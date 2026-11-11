package ansiblehttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/ansible"
)

const (
	collectionRoute = "/api/v3/collections/weave/sample/"
	versionRoute    = collectionRoute + "versions/1.2.3/"
	artifactRoute   = "/api/v3/plugin/ansible/content/published/collections/artifacts/" +
		"weave-sample-1.2.3.tar.gz"
)

var archivePath = ansiblefmt.Path("weave", "sample", "1.2.3")

// refusedWith asserts the download policy's 404 in the Artifactory error
// envelope, with message.
func refusedWith(t *testing.T, w *httptest.ResponseRecorder, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	requireStatus(t, w, http.StatusNotFound)
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 ||
		body.Errors[0].Status != http.StatusNotFound || body.Errors[0].Message != message {
		t.Fatalf("body %s, want 404 %q", w.Body, message)
	}
}

func blackedOut(key, path string) string {
	return fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.",
		key, key, path)
}

func rejected(key, path string) string {
	return fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its "+
		"include/exclude pattern settings.", key, key, path)
}

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

// describe deploys the sample collection to the local repository key, its
// description naming the repository so an answer shows which member served
// it.
func (f *fixture) describe(key string) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.items.Deploy(f.t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Path: archivePath}, Body: bytes.NewReader(f.data), By: "admin",
	}); err != nil {
		f.t.Fatal(err)
	}
	p, err := f.db.Packages().Find(f.t.Context(), key, "weave.sample")
	if err != nil {
		f.t.Fatal(err)
	}
	vs, err := f.db.Packages().Versions(f.t.Context(), p.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var m ansiblefmt.Metadata
	if err := json.Unmarshal(vs[0].Metadata, &m); err != nil {
		f.t.Fatal(err)
	}
	m.Description = key
	vs[0].Metadata, _ = json.Marshal(m)
	if _, err := f.db.Packages().PutVersion(f.t.Context(), vs[0]); err != nil {
		f.t.Fatal(err)
	}
}

// A blacked-out local repository's Galaxy metadata is refused before it is
// built, naming the request's path (R2a), and its archive with the
// blackout's message rather than "Could not find resource" (R6); a release
// whose archive the patterns reject stays listed and only its download is
// refused (R3). A virtual repository leaves a blacked-out member out of its
// listings, passes over a member that refuses the archive, and its own
// patterns refuse the archive with "No repository found" (R5).
func TestAccGalaxyDownloadPolicyLocalAndVirtual(t *testing.T) {
	f := newFixture(t)
	second := *repo.Defaults(&repo.Config{Key: "second", Class: repo.Local, PackageType: repo.Ansible})
	virtual := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Ansible,
		Repositories: []string{f.repo.Key, second.Key},
	})
	for _, c := range []repo.Config{second, virtual} {
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	f.describe(f.repo.Key)
	f.describe(second.Key)
	local := func(route string) *httptest.ResponseRecorder {
		t.Helper()
		return f.do(http.MethodGet, prefix+route, nil, "", "")
	}
	viaVirtual := func(route string) *httptest.ResponseRecorder {
		t.Helper()
		return f.do(http.MethodGet, "/artifactory/api/ansible/virtual"+route, nil, "", "")
	}
	describedBy := func(w *httptest.ResponseRecorder, key string) {
		t.Helper()
		requireStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), `"description":"`+key+`"`) {
			t.Fatalf("not %s's release: %s", key, w.Body)
		}
	}

	f.patch(f.repo.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, route := range []string{collectionRoute, collectionRoute + "versions/", versionRoute} {
		refusedWith(t, local(route),
			blackedOut(f.repo.Key, strings.Trim(route, "/")))
	}
	refusedWith(t, local(artifactRoute), blackedOut(f.repo.Key, archivePath))
	requireStatus(t, f.do(http.MethodGet, prefix+versionRoute, nil, "", "denied"),
		http.StatusUnauthorized)
	requireStatus(t, local("/"), http.StatusOK)
	describedBy(viaVirtual(versionRoute), second.Key)
	requireStatus(t, viaVirtual(artifactRoute), http.StatusOK)

	f.patch(f.repo.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("**/*.tar.gz")
	})
	requireStatus(t, local(collectionRoute), http.StatusOK)
	if w := local(collectionRoute + "versions/"); !strings.Contains(w.Body.String(),
		`{"version":"1.2.3"}`) {
		t.Fatalf("versions left out the release: %d %s", w.Code, w.Body)
	}
	describedBy(local(versionRoute), f.repo.Key)
	refusedWith(t, local(artifactRoute), rejected(f.repo.Key, archivePath))
	describedBy(viaVirtual(versionRoute), f.repo.Key)
	w := viaVirtual(artifactRoute)
	requireStatus(t, w, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), f.data) {
		t.Fatal("the next member's archive changed")
	}

	f.patch(f.repo.Key, func(c *repo.Config) { c.ExcludesPattern = new("") })
	f.patch(virtual.Key, func(c *repo.Config) { c.ExcludesPattern = new("**/*.tar.gz") })
	describedBy(viaVirtual(versionRoute), f.repo.Key)
	refusedWith(t, viaVirtual(artifactRoute),
		"No repository found to serve the request for virtual:"+archivePath)
}

// A remote repository's blackout refuses its Galaxy metadata naming the
// request's path, and its archive download naming the archive, without
// asking the upstream. A version whose archive the remote's patterns reject
// stays listed, where the listing answered 502, and only the archive's
// download is refused (R3, R4). A virtual repository leaves a blacked-out
// remote member out of its listings.
func TestAccGalaxyDownloadPolicyRemote(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	f.patch(c.Key, func(c *repo.Config) { c.BlackedOut = new(true) })
	refusedWith(t, f.do(http.MethodGet, base+collectionRoute+"versions/", nil, "", ""),
		blackedOut(c.Key, "api/v3/collections/weave/sample/versions"))
	refusedWith(t, f.do(http.MethodGet, base+artifactRoute, nil, "", ""),
		blackedOut(c.Key, archivePath))
	if n := len(up.Requests()); n != 0 {
		t.Fatalf("a blacked-out remote asked its upstream %d times", n)
	}

	f.describe(f.repo.Key)
	virtual := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Ansible,
		Repositories: []string{c.Key, f.repo.Key},
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodGet, "/artifactory/api/ansible/virtual"+versionRoute, nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"description":"`+f.repo.Key+`"`) {
		t.Fatalf("not the local member's release: %s", w.Body)
	}

	f.patch(c.Key, func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("collections/weave/sample/1.2.3/**")
	})
	w = f.do(http.MethodGet, base+collectionRoute+"versions/", nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `{"version":"1.2.3"}`) {
		t.Fatalf("versions left out the release: %s", w.Body)
	}
	w = f.do(http.MethodGet, base+versionRoute, nil, "", "")
	requireStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"download_url"`) {
		t.Fatalf("version: %s", w.Body)
	}
	refusedWith(t, f.do(http.MethodGet, base+artifactRoute, nil, "", ""),
		rejected(c.Key, archivePath))
}
