package cranhttp

// This file holds the fixtures of the tests that call the protocol's code
// without the router. A test here cannot build the router, which imports this
// package, so the item world is a kittest world, and do serves a request
// through the routes the requests need, mounted as the router mounts them: the
// artifacts module's item routes, then the artifactory spec's strict handler
// with the CRAN partial as its only implementation, over one item kit. The
// caller is in the context as the router's authentication leaves it, and the
// access module is accesstest's, over the world's store.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	artifactshttp "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Config is the part of the router's configuration that the tests here
// set, which newKit and newItemWorld pass on as the router does.
type Config struct {
	Store                   serverStore
	Blobs                   blob.Store
	Access                  accessapi.API
	CranArchiveMoverEnabled bool
}

// serverStore is the store the router's worlds hold: the artifacts
// partial's, which holds the item kit's, and the access module's, which the
// router binds over the same store.
type serverStore interface {
	artifactshttp.Store
	accesstest.Store
}

// newKit returns the item kit over c. The router's overlays and private
// predicate are left out: no test here serves an item GET.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{Store: c.Store, Blobs: c.Blobs, Access: c.Access})
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

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

// The archive paths and the migration URL the tests name, spelled as the
// root's tests spell them.
const (
	cranLegacyPath  = "src/contrib/Archive/assertthat/0.2.1/assertthat_0.2.1.tar.gz"
	cranArchivePath = "src/contrib/Archive/assertthat/assertthat_0.2.1.tar.gz"
	cranMoveURL     = "/artifactory/api/cran/cran-migrate/move-archives"
)

// cranServer is the artifactory spec's strict server with the CRAN partial
// as its only implementation: every other operation is the generated
// stub's 501. The partial sits at depth 1 and the stubs at depth 2, as in
// the router's composite.
type cranServer struct {
	*CranAPI
	cranStubs
}

// cranStubs holds the artifactory spec's generated stubs.
type cranStubs struct{ artgen.UnimplementedStrict }

// itemWorld is a store and blob store, and the routes that serve their
// requests as the router would.
type itemWorld struct {
	t     *testing.T
	db    *inmem.Store
	blobs blob.Store
	h     http.Handler
}

// newItemWorld returns a kittest world, with the handler that do serves
// through built from it as the router builds its own: tune adjusts the
// configuration first, the access module is bound over the store, the
// artifacts partial's item routes come before the strict handler, the CRAN
// partial embeds the artifacts partial's kit, and binding and handler
// errors are written as the router writes them.
func newItemWorld(t *testing.T, tune ...func(*Config)) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	c := Config{Store: k.Store, Blobs: k.Blobs}
	for _, f := range tune {
		f(&c)
	}
	c = withAccess(t, c)
	art := artifactshttp.New(artifactshttp.Deps{
		Store: c.Store,
		Deps:  itemkit.Deps{Blobs: c.Blobs, Access: c.Access},
	})
	ops := CranAPI{Kit: art.Kit, ArchiveMoverEnabled: c.CranArchiveMoverEnabled}
	r := chi.NewRouter()
	art.RegisterItemRoutes(r)
	opts := artgen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	h := artgen.HandlerWithOptions(
		artgen.NewStrictHandlerWithOptions(cranServer{CranAPI: &ops}, nil, opts),
		artgen.ChiServerOptions{
			BaseURL:          "/artifactory",
			BaseRouter:       r,
			ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
		},
	)
	return &itemWorld{t: t, db: k.Store, blobs: k.Blobs, h: h}
}

// do serves method on path, with body, as the named caller: admin is
// kittest's administrator, whom every question allows, and any other name
// a user whom none allows.
func (w *itemWorld) do(method, path, as, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	p, d := kittest.Admin, kittest.AdminAccess().Decider
	if as != kittest.Admin.Name {
		p, d = kernel.Principal{Name: as}, &accessapitest.Decider{}
	}
	r := httptest.NewRequestWithContext(w.t.Context(), method, path, strings.NewReader(body))
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// item returns the stored item at p in key, or its cache when cached, and
// fails the test when there is none.
func (w *itemWorld) item(key string, cached bool, p string) types.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(w.t.Context(), key, cached, p)
	if err != nil {
		w.t.Fatalf("item %s/%s: %v", key, p, err)
	}
	return it
}
