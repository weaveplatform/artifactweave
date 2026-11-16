package npm_test

// This file holds the npm route's answers to a caller who may read a
// remote but not fill its cache (#1587) and to a remote whose upstream is
// assumed offline (#1601).

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// npmRemoteWorld is the harness's remote world with an npm remote over its
// upstream, and the user "viewer", password "pw", who may read the remote
// and nothing more.
func npmRemoteWorld(t *testing.T) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.NPM, URL: w.Upstream.URL,
	})
	w.Update()
	ctx := t.Context()
	if err := w.Runtime.Store.Users().
		Create(ctx, accesstest.User{Name: "viewer", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "remote-view",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"viewer": api.ActionRead}},
			Targets: map[string]accesstest.Target{"remote": {}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestAccNPMRemoteReadAloneMisses (#1587): a package document and a
// tarball the upstream holds and the cache does not answer the npm route's
// own 404 to a caller with Read alone, whom 7.x refuses the deploy to
// '<remote>-cache:.npm/<name>/package.json' that filling the cache needs
// (JFrog's KB: such installs "fail with a 404 instead of a 403"); the npm
// client's body is [I]. The upstream is asked for the document's info,
// never its body, and a caller with Deploy/Cache is served it. Before, the
// reader filled the cache: 200.
func TestAccNPMRemoteReadAloneMisses(t *testing.T) {
	w := npmRemoteWorld(t)
	doc := `{"name":"left","versions":{"1.0.0":{"name":"left","version":"1.0.0",` +
		`"dist":{"tarball":"` + w.Upstream.URL + `/left/-/left-1.0.0.tgz"}}},"dist-tags":{"latest":"1.0.0"}}`
	w.Upstream.Set("/left", fakeupstream.Resource{
		Body: []byte(doc), Header: http.Header{"Content-Type": {"application/json"}},
	})
	w.Upstream.Set("/left/-/left-1.0.0.tgz", fakeupstream.Resource{Body: []byte("tarball")})
	for _, path := range []string{"/artifactory/api/npm/remote/left", "/artifactory/api/npm/remote/left/-/left-1.0.0.tgz"} {
		rec := w.Request(http.MethodGet, path, "viewer", "")
		if rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"Not Found"}`+"\n" {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body)
		}
	}
	if n := w.Upstream.Count(http.MethodGet, ""); n != 0 {
		t.Fatalf("a read-only caller downloaded: %v", w.Upstream.Requests())
	}
	harness.WantStatus(
		t,
		w.Request(http.MethodGet, "/artifactory/api/npm/remote/left", "reader", ""),
		http.StatusOK,
	)
}

// TestAccNPMRemoteAssumedOfflineMisses (#1601): once connection failures
// have the upstream assumed offline, a package document the cache does not
// hold answers the npm route's own 404 [I], as 7.x's internalGetInfo
// answers it not found without asking the upstream. Before, 502 "Bad
// Gateway".
func TestAccNPMRemoteAssumedOfflineMisses(t *testing.T) {
	w := npmRemoteWorld(t)
	w.Upstream.Set("/down", fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}})
	for range 10 {
		harness.WantStatus(
			t,
			w.Request(http.MethodGet, "/artifactory/api/npm/remote/down", "reader", ""),
			http.StatusBadGateway,
		)
	}
	asked := len(w.Upstream.Requests())
	rec := w.Request(http.MethodGet, "/artifactory/api/npm/remote/other", "reader", "")
	if rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"Not Found"}`+"\n" {
		t.Fatalf("assumed offline: %d %q", rec.Code, rec.Body)
	}
	if len(w.Upstream.Requests()) != asked {
		t.Fatalf("an assumed-offline upstream was asked: %v", w.Upstream.Requests()[asked:])
	}
}
