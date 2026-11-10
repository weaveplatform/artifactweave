package nuget_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const nugetV3 = "/artifactory/api/nuget/v3/"

// Keep the protocol tests' fixture vocabulary local while sharing the seeded
// principals, request behavior and reproducible package bytes with root tests.
var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db store.Store) { t.Helper(); support.Seed(t, db) }

func do(
	t *testing.T, h http.Handler, method, path string, headers map[string]string, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func basic(user, password string) map[string]string { return support.Basic(user, password) }
func bearerAuth(token string) map[string]string     { return support.Bearer(token) }
func nugetForm(pkg []byte) string                   { return support.NuGetForm(pkg) }

func contentTypeOf(body string) (contentType, rest string) {
	if b, ok := strings.CutPrefix(body, "multipart:"); ok {
		boundary, form, _ := strings.Cut(b, "\n")
		return "multipart/form-data; boundary=" + boundary, form
	}
	return "application/json", body
}

func nugetSymbolsPackage(id, version string) []byte {
	return support.NuGetSymbolsPackage(id, version)
}

func nugetPackage(id, version, more string) []byte {
	return support.NuGetPackage(id, version, more)
}

func artifactoryError(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	return support.ArtifactoryError(t, rec)
}

// remoteWorld is the harness's remote world under the fields the tests had
// in migration/app: a test changes c and saves it with update, or replaces h
// with a router built with other options.
type remoteWorld struct {
	t  *testing.T
	rt *app.Runtime
	h  http.Handler
	up *fakeupstream.Server
	c  repo.Config
}

func newRemoteWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	return &remoteWorld{t: w.T, rt: w.Runtime, h: w.H, up: w.Upstream, c: w.Repo}
}

// world is w as the harness's remote world, with the handler and the
// repository the test may have replaced.
func (w *remoteWorld) world() *harness.RemoteWorld {
	return &harness.RemoteWorld{T: w.t, Runtime: w.rt, H: w.h, Upstream: w.up, Repo: w.c}
}

func (w *remoteWorld) request(
	method, path, user, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.world().Request(method, path, user, body, headers...)
}

func (w *remoteWorld) update() {
	w.t.Helper()
	w.world().Update()
}

func remoteStatus(t *testing.T, r *httptest.ResponseRecorder, code int) {
	t.Helper()
	harness.WantStatus(t, r, code)
}
