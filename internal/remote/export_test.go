package remote

import (
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// These names open policyFixture to the external tests (package remote_test).
// A test that needs the format registry or the Maven, Go or CocoaPods slice
// belongs there: once those slices import this package, an in-package test
// that imports them is an import cycle.

// PolicyFixture is policyFixture: a generic remote repository behind a fake
// upstream, read through an item service without a format registry.
var PolicyFixture = policyFixture

// Policy is the policy the fixture reads through.
func (w *policyWorld) Policy() *Policy { return w.p }

// Upstream is the fake upstream the fixture's repository points at.
func (w *policyWorld) Upstream() *fakeupstream.Server { return w.up }

// Clock is the fixture's clock, which the policy and the upstream share.
func (w *policyWorld) Clock() *fakeupstream.Clock { return w.clock }

// DB is the fixture's store.
func (w *policyWorld) DB() *inmem.Store { return w.db }

// Request is a request for path, of kind, in the fixture's repository.
func (w *policyWorld) Request(path string, kind source.ResourceKind) source.Request {
	return w.request(path, kind)
}

// Read reads q through the policy with the fixture's permit.
func (w *policyWorld) Read(q source.Request) (source.Result, error) { return w.read(q) }

// MustRead reads q and fails the test unless the result holds want.
func (w *policyWorld) MustRead(q source.Request, want string) source.Result {
	return w.mustRead(q, want)
}

// Kind is the retrieval class the policy gives q.
func (p *Policy) Kind(q source.Request) types.RemoteKind { return p.kind(q) }

// The Smart registry helpers, for smart_registry_test.go: that test passes
// the Docker slice's parser, and an in-package test importing the slice is an
// import cycle.
var (
	SmartRegistryGrant = smartRegistryGrant
	SmartPropertyPath  = smartPropertyPath
	SmartStoragePath   = smartStoragePath
)
