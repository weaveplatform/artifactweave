package maven_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/app"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/app/config"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db *inmem.Store)       { t.Helper(); support.Seed(t, db) }
func basic(user, password string) map[string]string { return support.Basic(user, password) }
func bearerAuth(value string) map[string]string     { return support.Bearer(value) }
func contentTypeOf(body string) (string, string)    { return "application/json", body }

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func fixtureRepos(name string, kind repo.PackageType) []repo.Config {
	local := name + "-local"
	return []repo.Config{
		{Key: local, Class: repo.Local, PackageType: kind},
		{Key: name + "-remote", Class: repo.Remote, PackageType: kind, URL: "https://up.example"},
		{
			Key: name + "-virtual", Class: repo.Virtual, PackageType: kind,
			Repositories: []string{local}, DefaultDeploymentRepo: local,
		},
	}
}

// mavenServer assembles the real router with only the Maven repository needed by
// this package. Each matrix/status fixture adds its own permission model.
func mavenServer(
	t *testing.T,
	tune ...harness.Tune,
) (http.Handler, *inmem.Store, *token.Service) {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(t.Context(), model.User{Name: "user", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&mavenRepos[0])); err != nil {
		t.Fatal(err)
	}
	if err := db.Instance().Init(t.Context(), instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(t.Context(), db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a,
		Tokens: tokens, PasswordHash: testHash,
	}
	for _, fn := range tune {
		fn(&cfg)
	}
	return harness.Router(cfg), db, tokens
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

// testConfig is the configuration the harness opens a runtime from: the
// default one with a temporary data directory.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	return harness.AppConfig(t)
}

// openRuntime opens the application runtime with the harness's costs. The
// restart tests close it and open it again themselves, so it is not closed
// when the test ends.
func openRuntime(ctx context.Context, cfg config.Config, log *slog.Logger) (*app.Runtime, error) {
	return app.OpenRuntime(ctx, cfg, log, harness.AppOptions())
}
