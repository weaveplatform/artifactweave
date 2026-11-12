package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// The P8 package paths answer errors in Artifactory's envelope, as 7.x
// does under /api/pypi/, /api/nuget/ and /api/helm/ (decisions 0037 to
// 0039), and so does every JFrog operation beside a registry with an
// envelope of its own. Only the Terraform registry's routes keep theirs.
func TestAccP8ErrorProfiles(t *testing.T) {
	for path, want := range map[string]apierror.Profile{
		"/artifactory/api/pypi/r/simple/x/":                        apierror.ProfileArtifactory,
		"/artifactory/api/pypi/r/reindex":                          apierror.ProfileArtifactory,
		"/artifactory/api/pypi/r/pypi/x/json":                      apierror.ProfileArtifactory,
		"/artifactory/api/nuget/v3/r/index.json":                   apierror.ProfileArtifactory,
		"/artifactory/api/nuget/r/FindPackagesById()":              apierror.ProfileArtifactory,
		"/artifactory/api/nuget/r/reindex":                         apierror.ProfileArtifactory,
		"/artifactory/api/helm/r/index.yaml":                       apierror.ProfileArtifactory,
		"/artifactory/api/helm/r/reindex":                          apierror.ProfileArtifactory,
		"/artifactory/api/helm/r/charts/app-1.0.0.tgz/reindex":     apierror.ProfileArtifactory,
		"/artifactory/api/terraform/r/reindex":                     apierror.ProfileArtifactory,
		"/artifactory/api/terraform/v1/modules/r__ns/n/s/versions": apierror.ProfileTerraformRegistry,
		"/artifactory/api/terraform/r/v1/modules/ns/n/s/versions":  apierror.ProfileTerraformRegistry,
		"/artifactory/api/terraform/r/providers/h/ns/t/index.json": apierror.ProfileTerraformRegistry,
		"/artifactory/api/terraform/r/reindex/x":                   apierror.ProfileTerraformRegistry,
		"/artifactory/api/terraform//reindex":                      apierror.ProfileTerraformRegistry,
		"/artifactory/api/terraform/reindex":                       apierror.ProfileTerraformRegistry,
		"/artifactory/api/npm/r/reindex":                           apierror.ProfileArtifactory,
		"/artifactory/api/go/r/example.com/m/@v/list":              apierror.ProfileText,
	} {
		if got := errorProfileFor(path); got != want {
			t.Errorf("errorProfileFor(%s) = %v, want %v", path, got, want)
		}
	}
}

// The pub, Swift and Agent Packages paths answer every error in their
// protocol's envelope, the middleware's included, and the JFrog reindex
// operation beside pub and Swift keeps Artifactory's (#1249).
func TestAccProtocolErrorProfiles(t *testing.T) {
	for path, want := range map[string]apierror.Profile{
		"/artifactory/api/pub/r/api/packages/x":               apierror.ProfilePub,
		"/artifactory/api/pub/r/packages/x/versions/1.tar.gz": apierror.ProfilePub,
		"/artifactory/api/pub/r/reindex":                      apierror.ProfileArtifactory,
		"/artifactory/api/pub/r/reindex/":                     apierror.ProfileArtifactory,
		"/artifactory/api/pub/r/reindex/x":                    apierror.ProfilePub,
		"/artifactory/api/pub//reindex":                       apierror.ProfilePub,
		"/artifactory/api/swift/r/acme/widget":                apierror.ProfileSwiftRegistry,
		"/artifactory/api/swift/r/reindex":                    apierror.ProfileArtifactory,
		"/artifactory/api/swift/r/acme/reindex":               apierror.ProfileSwiftRegistry,
		"/artifactory/api/agentpackages/r/v1/search":          apierror.ProfileAPM,
		"/artifactory/api/agentpackages/r/reindex":            apierror.ProfileAPM,
	} {
		if got := errorProfileFor(path); got != want {
			t.Errorf("errorProfileFor(%s) = %v, want %v", path, got, want)
		}
	}
}

// The authentication middleware's 401 and 403 on the pub, Swift and Agent
// Packages paths answer in the protocol's envelope with its content type, and
// pub's 401 with the Bearer realm="pub" challenge dart pub reads (#1249).
// Their reindex operations keep Artifactory's envelope and Basic challenge.
// Under /v2 a 403 carries the distribution specification's DENIED (#1248).
func TestAccProtocolMiddlewareEnvelopes(t *testing.T) {
	w := newItemWorld(t, false)
	if err := w.db.Users().Create(t.Context(), accesstest.User{
		Name: "locked", PasswordHash: cheapHash, Status: accesstest.UserLocked,
	}); err != nil {
		t.Fatal(err)
	}
	const (
		pub      = "/artifactory/api/pub/libs-local/api/packages/widget"
		swift    = "/artifactory/api/swift/libs-local/acme/widget"
		apm      = "/artifactory/api/agentpackages/libs-local/v1/search"
		locked   = `User is Locked.\nContact System Administrator to Unlock The Account.`
		problem  = "application/problem+json; charset=utf-8"
		pubType  = "application/vnd.pub.v2+json"
		pubRealm = `Bearer realm="pub"`
	)
	for _, c := range []struct {
		method, path                 string
		headers                      map[string]string
		status                       int
		contentType, challenge, body string
	}{
		{
			http.MethodGet, pub, nil, 401, pubType, pubRealm,
			`{"error":{"code":"UNAUTHORIZED","message":"Authentication is required"}}`,
		},
		{
			http.MethodGet, pub, basic("admin", "wrong"), 401, pubType, pubRealm,
			`{"error":{"code":"UNAUTHORIZED","message":"Bad credentials"}}`,
		},
		{
			http.MethodGet, pub, basic("locked", "pw"), 403, pubType, "",
			`{"error":{"code":"FORBIDDEN","message":"` + locked + `"}}`,
		},
		{
			http.MethodGet, swift, nil, 401, problem, apierror.BasicChallenge,
			`{"detail":"Authentication is required","status":401,"title":"Unauthorized"}`,
		},
		{
			http.MethodGet, swift, basic("reader", "wrong"), 401, problem, apierror.BasicChallenge,
			`{"detail":"Bad credentials","status":401,"title":"Unauthorized"}`,
		},
		{
			http.MethodGet, swift, basic("locked", "pw"), 403, problem, "",
			`{"detail":"` + locked + `","status":403,"title":"Forbidden"}`,
		},
		{
			http.MethodGet, apm, nil, 401, problem, apierror.BasicChallenge,
			`{"detail":"Authentication is required","status":401,"title":"Unauthorized"}`,
		},
		{
			http.MethodGet, apm, basic("deployer", "wrong"), 401, problem, apierror.BasicChallenge,
			`{"detail":"Bad credentials","status":401,"title":"Unauthorized"}`,
		},
		{
			http.MethodGet, apm, basic("locked", "pw"), 403, problem, "",
			`{"detail":"` + locked + `","status":403,"title":"Forbidden"}`,
		},
		{
			http.MethodPost, "/artifactory/api/pub/libs-local/reindex", nil, 401,
			"application/json", apierror.BasicChallenge,
			`{"errors":[{"message":"Authentication is required","status":401}]}`,
		},
		{
			http.MethodPost, "/artifactory/api/swift/libs-local/reindex", nil, 401,
			"application/json", apierror.BasicChallenge,
			`{"errors":[{"message":"Authentication is required","status":401}]}`,
		},
		{
			http.MethodGet, "/v2/", basic("locked", "pw"), 403, "application/json", "",
			`{"errors":[{"code":"DENIED","message":"` + locked + `"}]}`,
		},
	} {
		rec := do(t, w.h, c.method, c.path, c.headers, "")
		if rec.Code != c.status || rec.Body.String() != c.body+"\n" ||
			rec.Header().Get("Content-Type") != c.contentType ||
			rec.Header().Get("WWW-Authenticate") != c.challenge {
			t.Errorf("%s %s %v: %d %q %q %s, want %d %q %q %s",
				c.method, c.path, c.headers, rec.Code, rec.Header().Get("Content-Type"),
				rec.Header().Get("WWW-Authenticate"), rec.Body,
				c.status, c.contentType, c.challenge, c.body)
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
		if p, ok := kernel.FromContext(r.Context()); ok {
			_, _ = io.WriteString(w, "as "+p.Name)
			return
		}
		_, _ = io.WriteString(w, "as nobody")
	}
	return pkgroute.Protocol{
		Name: "hostfmt", Types: []repo.PackageType{repo.Generic},
		ErrorProfile: apierror.ProfileTerraformRegistry,
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
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(RouterConfig{
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
