package p2http

// This file holds the fixtures of the tests that call the protocol's code
// without the router. A test here cannot build the router, which imports this
// package, so the item world is a kittest world, and the access module is
// accesstest's, over the world's store.

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Config is the part of the router's configuration that the tests here
// set, which newKit passes on to the item kit as the router does.
type Config struct {
	Blobs         blob.Store
	RemoteSources *source.Service
}

// newKit returns the item kit over c. The router's overlays are left out:
// the tests call the overlay's reads directly.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{Blobs: c.Blobs, RemoteSources: c.RemoteSources})
}

// authorizerOver is the access module's Authorizer over st.
func authorizerOver(t *testing.T, st accesstest.Store) accessapi.Authorizer {
	t.Helper()
	return accesstest.Over(t, st).API().Authorizer
}

// itemWorld is the store and blob store of the router's item world, for
// the tests that call the protocol's code over them.
type itemWorld struct {
	db    *inmem.Store
	blobs blob.Store
}

// newItemWorld returns a kittest world.
func newItemWorld(t *testing.T) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	return &itemWorld{db: k.Store, blobs: k.Blobs}
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

// sourceBody is a response body that counts how often it is closed.
type sourceBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *sourceBody) Close() error { b.closes.Add(1); return nil }

// p2ExternalComposite is a composite metadata file with one child on an
// external origin, spelled as the root's tests spell it.
const p2ExternalComposite = `<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="https://child.example/site"/></children></repository>`
