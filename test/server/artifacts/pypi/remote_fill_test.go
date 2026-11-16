package pypi_test

// This file holds the PyPI route's answer to a caller who may read a
// remote but not fill its cache (#1587).

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

// TestAccPypiRemoteReadAloneMisses (#1587): a simple page the upstream
// holds and the cache does not answers the PyPI route's own 404 to a
// caller with Read alone, as RTFACT-30824 shows for a read-only user: 7.x
// refuses the deploy to '<remote>-cache:.pypi/<name>.html' that filling
// the cache needs (JFrog's KB "How to differentiate between Read and
// Deploy/Cache permission on remote repositories"); the body is [I]. A
// caller with Deploy/Cache is served the page. Before, the reader filled
// the cache: 200.
func TestAccPypiRemoteReadAloneMisses(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.PyPI, URL: w.Upstream.URL,
		PyPIRegistryURL: w.Upstream.URL,
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
	page := `<!DOCTYPE html><html><body><a href="../../packages/left-1.0.tar.gz">left-1.0.tar.gz</a></body></html>`
	w.Upstream.Set("/simple/left/", fakeupstream.Resource{
		Body: []byte(page), Header: http.Header{"Content-Type": {"text/html"}},
	})
	const path = "/artifactory/api/pypi/remote/simple/left/"
	rec := w.Request(http.MethodGet, path, "viewer", "")
	if want := `{"errors":[{"message":"Not Found","status":404}]}` + "\n"; rec.Code != http.StatusNotFound ||
		rec.Body.String() != want {
		t.Fatalf("read alone: %d %q, want 404 %q", rec.Code, rec.Body, want)
	}
	if n := w.Upstream.Count(http.MethodGet, ""); n != 0 {
		t.Fatalf("a read-only caller downloaded: %v", w.Upstream.Requests())
	}
	harness.WantStatus(t, w.Request(http.MethodGet, path, "reader", ""), http.StatusOK)
}
