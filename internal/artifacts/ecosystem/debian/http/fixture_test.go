package debianhttp

// This file holds the fixtures of the tests that call the protocol's code
// without the router. A test here cannot build the router, which imports this
// package, so the item world is a kittest world, and do serves a request
// through the artifactory spec's strict handler with the Debian partial as its
// only implementation, mounted as the router mounts it, once the caller is in
// the context as the router's authentication leaves it. The access module is
// accesstest's, over the world's store.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Config is the part of the router's configuration that the tests here
// set, which newKit passes on to the item kit as the router does.
type Config struct {
	Store  serverStore
	Blobs  blob.Store
	Access accessapi.API
}

// serverStore is the store the router's worlds hold: the item kit's, and
// the access module's, which the router binds over the same store.
type serverStore interface {
	itemkit.Store
	accesstest.Store
}

// newKit returns the item kit over c. The router's overlays are left out:
// no test here serves an item GET.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{Store: c.Store, Blobs: c.Blobs, Access: c.Access})
}

// withAccess returns c with the access module over its store, as the router
// binds it for a configuration that sets none, and, as there, without an
// authenticator, since the configuration names none: nothing can recheck a
// caller's credential.
func withAccess(t *testing.T, c Config) Config {
	t.Helper()
	c.Access = accesstest.Over(t, c.Store).API()
	c.Access.Authenticator = nil
	return c
}

// authorizerOver is the access module's Authorizer over st.
func authorizerOver(t *testing.T, st accesstest.Store) accessapi.Authorizer {
	t.Helper()
	return accesstest.Over(t, st).API().Authorizer
}

// cheapHash is a cheap hash of "pw", the password of every test principal.
var cheapHash = support.CheapHash

// cheapSeed seeds db with the built-in principals, admin's password "pw".
func cheapSeed(t *testing.T, db *inmem.Store) {
	t.Helper()
	support.Seed(t, db)
}

// debianServer is the artifactory spec's strict server with the Debian
// partial as its only implementation: every other operation is the
// generated stub's 501. The partial sits at depth 1 and the stubs at depth
// 2, as in the router's composite.
type debianServer struct {
	*DebianAPI
	debianStubs
}

// debianStubs holds the artifactory spec's generated stubs.
type debianStubs struct{ artgen.UnimplementedStrict }

// itemWorld is a store and blob store, and the strict handler that serves
// their operations as the router would.
type itemWorld struct {
	t     *testing.T
	db    *inmem.Store
	blobs blob.Store
	h     http.Handler
}

// newItemWorld returns a kittest world, with the handler that do serves
// through built from it as the router builds its own: the access module is
// bound over the store, and binding and handler errors are written as the
// router writes them.
func newItemWorld(t *testing.T) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	ops := DebianAPI{newKit(withAccess(t, Config{Store: k.Store, Blobs: k.Blobs}))}
	opts := artgen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	h := artgen.HandlerWithOptions(
		artgen.NewStrictHandlerWithOptions(debianServer{DebianAPI: &ops}, nil, opts),
		artgen.ChiServerOptions{
			BaseURL:          "/artifactory",
			BaseRouter:       chi.NewRouter(),
			ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
		},
	)
	return &itemWorld{t: t, db: k.Store, blobs: k.Blobs, h: h}
}

// do serves method on path, with body and the header pairs in headers, as
// the named caller: admin is kittest's administrator, whom every question
// allows, and any other name a user whom none allows.
func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	p, d := kittest.Admin, kittest.AdminAccess().Decider
	if as != kittest.Admin.Name {
		p, d = kernel.Principal{Name: as}, &accessapitest.Decider{}
	}
	r := httptest.NewRequestWithContext(w.t.Context(), method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// item returns the stored item at p in key, or its cache when cached, and
// fails the test when there is none.
func (w *itemWorld) item(key string, cached bool, p string) model.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(w.t.Context(), key, cached, p)
	if err != nil {
		w.t.Fatalf("item %s/%s: %v", key, p, err)
	}
	return it
}
