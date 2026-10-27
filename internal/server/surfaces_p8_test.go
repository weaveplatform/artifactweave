package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// The P8 package paths answer errors in Artifactory's envelope, as 7.x
// does under /api/pypi/, /api/nuget/ and /api/helm/ (decisions 0037 to
// 0039), and so does every JFrog operation beside a registry with an
// envelope of its own. Only the Terraform registry's routes keep theirs.
func TestAccP8Surfaces(t *testing.T) {
	for path, want := range map[string]apierror.Surface{
		"/artifactory/api/pypi/r/simple/x/":                        apierror.SurfaceArtifactory,
		"/artifactory/api/pypi/r/reindex":                          apierror.SurfaceArtifactory,
		"/artifactory/api/pypi/r/pypi/x/json":                      apierror.SurfaceArtifactory,
		"/artifactory/api/nuget/v3/r/index.json":                   apierror.SurfaceArtifactory,
		"/artifactory/api/nuget/r/FindPackagesById()":              apierror.SurfaceArtifactory,
		"/artifactory/api/nuget/r/reindex":                         apierror.SurfaceArtifactory,
		"/artifactory/api/helm/r/index.yaml":                       apierror.SurfaceArtifactory,
		"/artifactory/api/helm/r/reindex":                          apierror.SurfaceArtifactory,
		"/artifactory/api/helm/r/charts/app-1.0.0.tgz/reindex":     apierror.SurfaceArtifactory,
		"/artifactory/api/terraform/r/reindex":                     apierror.SurfaceArtifactory,
		"/artifactory/api/terraform/v1/modules/r__ns/n/s/versions": apierror.SurfaceTerraformRegistry,
		"/artifactory/api/terraform/r/v1/modules/ns/n/s/versions":  apierror.SurfaceTerraformRegistry,
		"/artifactory/api/terraform/r/providers/h/ns/t/index.json": apierror.SurfaceTerraformRegistry,
		"/artifactory/api/terraform/r/reindex/x":                   apierror.SurfaceTerraformRegistry,
		"/artifactory/api/terraform//reindex":                      apierror.SurfaceTerraformRegistry,
		"/artifactory/api/terraform/reindex":                       apierror.SurfaceTerraformRegistry,
		"/artifactory/api/npm/r/reindex":                           apierror.SurfaceArtifactory,
		"/artifactory/api/go/r/example.com/m/@v/list":              apierror.SurfaceText,
	} {
		if got := surfaceFor(path); got != want {
			t.Errorf("surfaceFor(%s) = %v, want %v", path, got, want)
		}
	}
}

// artifactoryError decodes the Artifactory envelope, failing on any other
// body, and returns its one error.
func artifactoryError(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	return support.ArtifactoryError(t, rec)
}

// Each P8 reindex operation, Helm's partial one included, answers its
// refusals in Artifactory's envelope, whichever envelope its protocol's
// routes use.
func TestAccReindexEnvelopes(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	for _, c := range []struct {
		method, path string
		headers      map[string]string
		expected     int
	}{
		{http.MethodPost, "/artifactory/api/pypi/missing/reindex", admin, http.StatusNotFound},
		{http.MethodPost, "/artifactory/api/pypi/reindex-nuget/reindex", admin, http.StatusBadRequest},
		{http.MethodPost, "/artifactory/api/pypi/reindex-pypi/reindex", nil, http.StatusUnauthorized},
		{http.MethodPost, "/artifactory/api/nuget/missing/reindex", admin, http.StatusNotFound},
		{http.MethodPost, "/artifactory/api/nuget/reindex-nuget-virtual/reindex", admin, http.StatusNotImplemented},
		{http.MethodPost, "/artifactory/api/helm/missing/reindex", admin, http.StatusNotFound},
		{http.MethodPost, "/artifactory/api/helm/reindex-helm-virtual/reindex", admin, http.StatusBadRequest},
		{http.MethodPost, "/artifactory/api/helm/missing/charts/reindex", admin, http.StatusNotFound},
		{http.MethodPost, "/artifactory/api/helm/reindex-helm/charts/reindex", nil, http.StatusUnauthorized},
		{http.MethodPost, "/artifactory/api/terraform/missing/reindex", admin, http.StatusNotFound},
		{http.MethodPost, "/artifactory/api/terraform/reindex-helm/reindex", admin, http.StatusBadRequest},
		{http.MethodPost, "/artifactory/api/terraform/reindex-terraform/reindex", basic("user", "pw"), http.StatusForbidden},
		{http.MethodPost, "/artifactory/api/terraform/reindex-terraform/reindex", nil, http.StatusUnauthorized},
		{http.MethodGet, "/artifactory/api/terraform/reindex-terraform/reindex", admin, http.StatusMethodNotAllowed},
	} {
		rec := do(t, h, c.method, c.path, c.headers, "")
		if rec.Code != c.expected {
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.path, rec.Code, c.expected, rec.Body)
			continue
		}
		if status, _ := artifactoryError(t, rec); status != c.expected {
			t.Errorf("%s %s: envelope status %d", c.method, c.path, status)
		}
	}
}

// hostFormat is a test protocol with two routes at the host root, one
// served before authentication, as Terraform's discovery document is.
func hostFormat() pkgroute.Protocol {
	serve := func(w http.ResponseWriter, r *http.Request, _ *pkgroute.Kit) {
		if p, ok := auth.FromContext(r.Context()); ok {
			_, _ = io.WriteString(w, "as "+p.Name)
			return
		}
		_, _ = io.WriteString(w, "as nobody")
	}
	return pkgroute.Protocol{
		Name: "hostfmt", Types: []repo.PackageType{repo.Generic},
		Surface: apierror.SurfaceTerraformRegistry,
		Host: []pkgroute.HostRoute{
			{
				Path: "/.well-known/hostfmt.json", Methods: []string{http.MethodGet},
				Unauthenticated: true, Serve: serve,
			},
			{
				Path:    "/.well-known/hostfmt-private.json",
				Methods: []string{http.MethodGet},
				Serve:   serve,
			},
		},
	}
}

// A host-root route is served without /artifactory/api/, and one declared
// unauthenticated reads no credentials, so neither their absence nor bad
// ones stop it.
func TestAccHostRouteAuthentication(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{
		CompatVersion: "7.155.0", Store: db, Auth: a,
		Protocols: []pkgroute.Protocol{hostFormat()},
	})
	for _, c := range []struct {
		method, path string
		headers      map[string]string
		expected     int
		body         string
	}{
		{http.MethodGet, "/.well-known/hostfmt.json", nil, http.StatusOK, "as nobody"},
		{http.MethodGet, "/.well-known/hostfmt.json", basic("admin", "nope"), http.StatusOK, "as nobody"},
		{
			http.MethodGet, "/.well-known/hostfmt.json",
			map[string]string{"Authorization": "Bearer expired"},
			http.StatusOK, "as nobody",
		},
		{http.MethodGet, "/.well-known/hostfmt.json", basic("admin", "pw"), http.StatusOK, "as nobody"},
		{
			http.MethodPost, "/.well-known/hostfmt.json", nil, http.StatusMethodNotAllowed,
			`{"errors":["method not allowed: POST /.well-known/hostfmt.json"]}` + "\n",
		},
		{http.MethodGet, "/.well-known/hostfmt-private.json", nil, http.StatusUnauthorized, ""},
		{http.MethodGet, "/.well-known/hostfmt-private.json", basic("admin", "pw"), http.StatusOK, "as admin"},
	} {
		rec := do(t, h, c.method, c.path, c.headers, "")
		if rec.Code != c.expected || c.body != "" && rec.Body.String() != c.body {
			t.Errorf("%s %s %v: %d %q, want %d %q",
				c.method, c.path, c.headers, rec.Code, rec.Body, c.expected, c.body)
		}
	}
}
