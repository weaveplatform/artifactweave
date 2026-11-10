package chef_test

// This file holds the Chef suite's fixtures: adapters over
// test/server/support/harness and test/server/support. The router is the
// harness's, so a change to its assembly reaches these tests without an edit
// here.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
func sourceIdentity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
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
