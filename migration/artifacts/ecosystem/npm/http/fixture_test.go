package npmhttp

// This file holds the fixtures of the tests that call NPMAPI's operations
// without the router, under the names the tests had in internal/server. A
// test here cannot build the router, which imports this package, so the
// access module is accesstest's over the test's store.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
)

// errBoom is the failure the fault stores inject.
var errBoom = errors.New("boom")

// p8Types are the package types reindexRecorder stands in for. The literal
// is the root's (internal/server/status_reindex_test.go).
var p8Types = []repo.PackageType{repo.PyPI, repo.NuGet, repo.Helm, repo.Terraform}

// Config is the part of the router's configuration that the tests here
// set, which newKit passes on to the item kit as the router does.
type Config struct {
	Store   serverStore
	Formats func(repo.PackageType) format.Handler
	Access  accessapi.API
}

// serverStore is the store the router's worlds hold: the item kit's, and
// the access module's, which the router binds over the same store.
type serverStore interface {
	itemkit.Store
	accesstest.Store
}

// newKit returns the item kit over c. The router's overlays and private
// path rule are left out: no test here serves an item GET.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{Store: c.Store, Formats: c.Formats, Access: c.Access})
}

// withAccess returns c with the access module over its store, as the router
// binds it for a configuration that sets none, and, as there, without an
// authenticator, since the configuration names none. A configuration
// without a store keeps no access module, as the router's does.
func withAccess(t *testing.T, c Config) Config {
	t.Helper()
	if c.Store == nil {
		return c
	}
	c.Access = accesstest.Over(t, c.Store).API()
	c.Access.Authenticator = nil
	return c
}

// reindexRecorder is the format of the P8 package types in these tests:
// the generic format made a PathReindexer that records each rebuild, as
// "<key>" or "<key>/<path>", and fails it with err.
type reindexRecorder struct {
	generic.Handler
	mu   sync.Mutex
	runs []string
	err  error
}

func (f *reindexRecorder) Reindex(_ context.Context, _ format.Env, c repo.Config) error {
	return f.record(c.Key)
}

func (f *reindexRecorder) ReindexPath(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	p string,
) error {
	return f.record(c.Key + "/" + p)
}

func (f *reindexRecorder) record(run string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return f.err
}

// recording picks f for the P8 package types and the real formats for the
// rest.
func recording(f *reindexRecorder) func(repo.PackageType) format.Handler {
	return func(t repo.PackageType) format.Handler {
		if slices.Contains(p8Types, t) {
			return f
		}
		return ecosystem.For(t)
	}
}
