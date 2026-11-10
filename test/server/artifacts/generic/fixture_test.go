package generic_test

// This file holds the generic remote suite's fixtures: adapters over
// test/server/support/harness under the names the tests had in
// internal/app. The runtime and the router are the harness's, so a change
// to their assembly reaches these tests without an edit here.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// remoteWorld is the harness's remote world under the fields the tests had
// in internal/app: a test changes c and saves it with update, or replaces h
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
