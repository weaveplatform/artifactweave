package rpmhttp

// This file holds the fixtures of the tests that call the overlay and its
// helpers without the router. A test here cannot build the router, which
// imports this package, so the item world is a kittest world, and do serves a
// request through the kit's item GET with TryRPMItem as its overlay, as the
// router's item route does once it has authenticated the caller. The access
// module is accesstest's, over the world's store.

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Config is the part of the router's configuration that the tests here
// set, which newKit passes on to the item kit as the router does.
type Config struct {
	Store         serverStore
	Blobs         blob.Store
	RemoteSources *source.Service
	Access        accessapi.API
}

// serverStore is the store the router's worlds hold: the item kit's, and
// the access module's, which the router binds over the same store.
type serverStore interface {
	itemkit.Store
	accesstest.Store
}

// newKit returns the item kit over c, with TryRPMItem as its one overlay:
// of the router's overlays, only it serves an RPM repository.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{
		Store:         c.Store,
		Blobs:         c.Blobs,
		RemoteSources: c.RemoteSources,
		Access:        c.Access,
		Overlays:      []itemkit.Overlay{TryRPMItem},
	})
}

// withAccess returns c with the access module over its store, as the
// router binds it for a configuration that sets none.
func withAccess(t *testing.T, c Config) Config {
	t.Helper()
	c.Access = accesstest.Over(t, c.Store).API()
	return c
}

// authorizerOver is the access module's Authorizer over st.
func authorizerOver(t *testing.T, st accesstest.Store) accessapi.Authorizer {
	t.Helper()
	return accesstest.Over(t, st).API().Authorizer
}

// itemWorld is a store and blob store, and the kit that serves their item
// requests as the router would.
type itemWorld struct {
	t     *testing.T
	db    *inmem.Store
	blobs blob.Store
	kit   *itemkit.Kit
}

// newItemWorld returns a kittest world, with the kit that do serves
// through built from it as the router builds its own: tune adjusts the
// configuration first, and the access module is bound over the store.
func newItemWorld(t *testing.T, tune ...func(*Config)) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	c := Config{Store: k.Store, Blobs: k.Blobs}
	for _, f := range tune {
		f(&c)
	}
	return &itemWorld{t: t, db: k.Store, blobs: k.Blobs, kit: newKit(withAccess(t, c))}
}

// do serves method on path, with body, through the kit's item GET, as the
// named caller: admin is kittest's administrator, whom every question
// allows, and any other name a user whom none allows, as the router's
// world seeds user.
func (w *itemWorld) do(method, path, as, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	p, d := kittest.Admin, kittest.AdminAccess().Decider
	if as != kittest.Admin.Name {
		p, d = kernel.Principal{Name: as}, &accessapitest.Decider{}
	}
	r := httptest.NewRequestWithContext(w.t.Context(), method, path, strings.NewReader(body))
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
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
