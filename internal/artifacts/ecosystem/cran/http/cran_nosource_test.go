package cranhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
)

// TestUnitCranRemoteReadsWithoutRemoteSources: on a server built without a
// remote source service, every remote CRAN read answers 500 "internal
// error", a fault of the server's own, rather than the upstream's 502 or a
// call through nil. A package request, to the remote repository or to a
// virtual one over it, reads the remote's index before the package and
// fails there, so only a direct call reaches the package read's own check.
func TestUnitCranRemoteReadsWithoutRemoteSources(t *testing.T) {
	w := kittest.New(t, inmem.New())
	kit := w.Kit(itemkit.Deps{
		Logger:   slog.New(slog.DiscardHandler),
		Overlays: []itemkit.Overlay{TryCranItem},
	})
	remote := *repo.Defaults(&repo.Config{
		Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran,
		URL: "https://cran.example",
	})
	virtual := *repo.Defaults(&repo.Config{
		Key: "cran-virtual", Class: repo.Virtual, PackageType: repo.Cran,
		Repositories: []string{remote.Key},
	})
	for _, c := range []repo.Config{remote, virtual} {
		if err := w.Store.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	const pkg = "src/contrib/assertthat_0.2.1.tar.gz"
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		kit.GetItem(rec, kittest.Request(t, http.MethodGet, path))
		return rec
	}
	for _, tc := range []struct {
		name  string
		serve func() *httptest.ResponseRecorder
	}{
		{"remote index", func() *httptest.ResponseRecorder {
			return get("/artifactory/cran-remote/src/contrib/PACKAGES")
		}},
		{"remote compressed index", func() *httptest.ResponseRecorder {
			return get("/artifactory/cran-remote/src/contrib/PACKAGES.gz")
		}},
		{"remote cache index", func() *httptest.ResponseRecorder {
			return get("/artifactory/cran-remote-cache/src/contrib/PACKAGES")
		}},
		{"virtual index over the remote", func() *httptest.ResponseRecorder {
			return get("/artifactory/cran-virtual/src/contrib/PACKAGES")
		}},
		{"remote package", func() *httptest.ResponseRecorder {
			return get("/artifactory/cran-remote/" + pkg)
		}},
		{"virtual package over the remote", func() *httptest.ResponseRecorder {
			return get("/artifactory/cran-virtual/" + pkg)
		}},
		{"direct remote package read", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r := kittest.Request(t, http.MethodGet, "/artifactory/cran-remote/"+pkg)
			req := itemkit.ItemRequest{Key: remote.Key, Path: pkg}
			CranAPI{Kit: kit}.serveRemoteCranPackage(rec, r, remote, false, req,
				cran.Package{Path: pkg}, kittest.Admin, kittest.AdminAccess())
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, tc.serve(), http.StatusInternalServerError, "internal error")
		})
	}
}

// wantError fails the test unless rec answered status with the Artifactory
// error envelope that carries message, as the item route writes an error.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}
