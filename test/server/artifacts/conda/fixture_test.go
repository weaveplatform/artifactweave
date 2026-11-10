package conda_test

// This file holds the Conda suite's fixtures: adapters over
// test/server/support/harness and test/server/support, and a copy of the
// failing reader of the router guards' fixtures. The router is the harness's,
// so a change to its assembly reaches these tests without an edit here.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

// itemWorld is the harness's item world under the fields the tests read.
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

func newItemWorld(t *testing.T, anonymous bool, tune ...harness.Tune) *itemWorld {
	t.Helper()
	w := harness.NewItemWorld(t, anonymous, tune...)
	return &itemWorld{t: w.T, h: w.H, db: w.DB, blobs: w.Blobs}
}

func (w *itemWorld) world() *harness.ItemWorld {
	return &harness.ItemWorld{T: w.t, H: w.h, DB: w.db, Blobs: w.blobs}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.world().Do(method, path, as, body, headers...)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	harness.WantStatus(t, rec, want)
}

// sourceIdentity resolves every remote repository to one fixed HTTP source
// identity, as the root fixture of the same name does.
func sourceIdentity(context.Context, repo.Config) (model.SourceIdentity, error) {
	return model.SourceIdentity{
		Kind:       model.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

// sourceBackend is a remote source transport made of one function.
type sourceBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func sha256Hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// opkgReadError fails every read, as the root opkg tests' reader of the
// same name does.
type opkgReadError struct{}

func (opkgReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

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
