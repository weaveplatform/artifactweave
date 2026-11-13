package agentpluginshttp

// This file holds the fixtures of the tests that serve the marketplace
// files. A test here cannot build the router, which imports this package,
// so the world is a kittest world, and do serves a request through a chi
// router that mounts what the router mounts for these files, in its order:
// the item kit's GET and HEAD at the item paths, with
// TryAgentMarketplaceItem as the kit's only overlay, then
// RegisterAgentMarketplaceRoutes. The caller is in the context as the
// router's authentication leaves it, with a fake decider. The kit has a
// work queue with the formats' handlers, which no worker runs: a download
// of a marketplace file runs the work it owes, as it does on a server
// whose workers have not reached it yet.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// The keys of the Agent Plugins repositories the tests store, beside
// kittest's generic ones.
const (
	pluginsLocal  = "plugins-local"
	pluginsRemote = "plugins-remote"
)

// pluginsRepo is an Agent Plugins repository of class under key.
func pluginsRepo(key string, class repo.Class) repo.Config {
	c := repo.Config{Key: key, Class: class, PackageType: repo.AgentPlugins}
	if class == repo.Remote {
		c.URL = "https://plugins.example"
	}
	return c
}

// apiPath is where the API serves the marketplace file name of the
// repository key.
func apiPath(key, name string) string {
	return "/artifactory/api/agentplugins/" + key + "/" + name
}

// itemPath is where the item routes serve the file name at the root of the
// repository key.
func itemPath(key, name string) string { return "/artifactory/" + key + "/" + name }

// itemWorld is a kittest world, the kit and the routes that serve its
// requests as the router would, and what the kit logged.
type itemWorld struct {
	t     *testing.T
	world *kittest.World[*inmem.Store]
	logs  *bytes.Buffer
	kit   *itemkit.Kit
	h     http.Handler
}

// newItemWorld returns a kittest world and the routes over an item kit
// built from it as the router builds its own, with a work queue over the
// world's store. tune adjusts the kit's dependencies first; the queue's
// handlers then run over the kit's store, so a faulty one fails the work
// too. The authorizer is a fake whose evaluation allows nothing, so a
// caller holds only what the decider in its request grants.
func newItemWorld(t *testing.T, tune ...func(*itemkit.Deps)) *itemWorld {
	t.Helper()
	k := kittest.New(t, inmem.New())
	logs := &bytes.Buffer{}
	q, err := work.New(k.Store, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := itemkit.Deps{
		Store:  k.Store,
		Blobs:  k.Blobs,
		Work:   q,
		Access: accessapi.API{Authorizer: &accessapitest.Authorizer{}},
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}
	for _, f := range tune {
		f(&d)
	}
	// Of the router's overlays only this one serves an Agent Plugins
	// repository, and the router lists it first.
	d.Overlays = []itemkit.Overlay{TryAgentMarketplaceItem}
	kit := itemkit.New(d)
	if d.Store != nil && d.Work != nil {
		if err := kit.Items(d.Store).RegisterWork(d.Work); err != nil {
			t.Fatal(err)
		}
	}
	r := chi.NewRouter()
	for _, p := range httpkit.ChiPatterns("/artifactory/{repoKey}/{filePath}") {
		r.Get(p, kit.GetItem)
		r.Head(p, kit.GetItem)
	}
	AgentPluginItems{Kit: kit}.RegisterAgentMarketplaceRoutes(r)
	return &itemWorld{t: t, world: k, logs: logs, kit: kit, h: r}
}

// caller is who a request comes from: the principal, and the decider the
// router's authentication binds to it. A nil decider leaves the
// principal's permissions to the kit's authorizer.
type caller struct {
	p kernel.Principal
	d accessapi.Decider
}

var (
	// admin is kittest's administrator, whom every question allows.
	admin = &caller{kittest.Admin, kittest.AdminAccess().Decider}
	// user is a user whom no question allows.
	user = &caller{kernel.Principal{Name: "user"}, &accessapitest.Decider{}}
	// anonymous is the anonymous user, whom no question allows.
	anonymous = &caller{kernel.Anonymous, &accessapitest.Decider{}}
)

// do serves method on target through the routes as the caller as, or with
// no caller in the context when as is nil, which the router's
// authentication never lets through.
func (w *itemWorld) do(method, target string, as *caller) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequestWithContext(w.t.Context(), method, target, nil)
	if as != nil {
		r = r.WithContext(accessapi.WithCaller(r.Context(), as.p, as.d))
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

// repos stores each of cs with its defaults and returns them as stored.
func (w *itemWorld) repos(cs ...repo.Config) []repo.Config {
	w.t.Helper()
	out := make([]repo.Config, 0, len(cs))
	for _, c := range cs {
		c = *repo.Defaults(&c)
		if err := w.world.Store.Repos().Create(w.t.Context(), c); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// put stores body as the file at path in the repository key, or in its
// cache when cached, without the format's hooks, as a file the generic
// lookup can serve.
func (w *itemWorld) put(key string, cached bool, path, body string) {
	w.t.Helper()
	w.world.Put(w.t, repo.Config{Key: key}, cached, path, []byte(body))
}

// indexed is when every plugin version here was indexed.
var indexed = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// plugin indexes version of the plugin name in plugins-local, with the
// manifests of harnesses, and queues the marketplace work, as the format's
// Deployed hook does for a bundle stored at the version's canonical path.
func (w *itemWorld) plugin(name, version string, harnesses ...string) {
	w.t.Helper()
	ctx := w.t.Context()
	p, err := w.world.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: pluginsLocal, Type: repo.AgentPlugins, Name: name, Created: indexed,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	meta, err := json.Marshal(agentplugins.Metadata{
		Name: name, Version: version, Path: agentplugins.Path(name, version),
		Harnesses: harnesses,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.world.Store.Packages().PutVersion(ctx, model.PackageVersion{
		PackageID: p.ID, Version: version, Created: indexed, Metadata: meta,
	}); err != nil {
		w.t.Fatal(err)
	}
	if err := w.world.Store.WorkQueue().Enqueue(
		ctx, agentplugins.KindMarketplace, pluginsLocal, nil, indexed,
	); err != nil {
		w.t.Fatal(err)
	}
}

// stored returns the item stored at the root of plugins-local under name.
func (w *itemWorld) stored(name string) model.Item {
	w.t.Helper()
	it, err := w.world.Store.Items().Get(w.t.Context(), pluginsLocal, false, name)
	if err != nil {
		w.t.Fatalf("%s is not stored: %v", name, err)
	}
	return it
}

// wantError fails the test unless rec answered status with the Artifactory
// error envelope that carries message, as the item routes write an error.
// A 401 must also carry Artifactory's Basic challenge, without which a
// client never sends the credentials it holds.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
	if challenge := rec.Header().Get("WWW-Authenticate"); status == http.StatusUnauthorized &&
		challenge != apierror.BasicChallenge {
		t.Errorf("401 challenge %q, want %q", challenge, apierror.BasicChallenge)
	}
}

// wantLogged fails the test unless the kit logged op with the injected
// fault as its error: a 500 names nothing internal, so the log is the only
// place that says which step failed.
func (w *itemWorld) wantLogged(t *testing.T, op string) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(w.logs.Bytes()))
	for dec.More() {
		var record struct {
			Msg   string `json:"msg"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&record); err != nil {
			t.Fatalf("log record: %v", err)
		}
		if record.Msg == op && strings.Contains(record.Error, errFault.Error()) {
			return
		}
	}
	t.Errorf("no %q logged with %q in:\n%s", op, errFault, w.logs)
}

// errFault is the failure the faulty stores and the failing authorizer
// here answer.
var errFault = errors.New("the store failed")

// failingAuthorizer evaluates no caller's permissions, as the access
// module does when it cannot read them.
var failingAuthorizer = &accessapitest.Authorizer{
	DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
		return nil, errFault
	},
}

// repoLookupFails is a store whose repository lookups fail.
type repoLookupFails struct{ itemkit.Store }

func (s repoLookupFails) Repos() port.RepoStore { return failingRepoGet{s.Store.Repos()} }

type failingRepoGet struct{ port.RepoStore }

func (failingRepoGet) Get(context.Context, string) (repo.Config, error) {
	return repo.Config{}, errFault
}

// itemReadFails is a store whose item lookups at the repository root fail
// once ok of them have answered, so the lookup a download makes after the
// format's own can fail alone.
type itemReadFails struct {
	itemkit.Store
	ok *int
}

func (s itemReadFails) Items() port.ItemStore { return &failingItemGet{s.Store.Items(), s.ok} }

type failingItemGet struct {
	port.ItemStore
	ok *int
}

func (s *failingItemGet) Get(
	ctx context.Context, key string, cached bool, p string,
) (model.Item, error) {
	if *s.ok == 0 {
		return model.Item{}, errFault
	}
	*s.ok--
	return s.ItemStore.Get(ctx, key, cached, p)
}

// packageListingFails is a store whose package listings fail.
type packageListingFails struct{ itemkit.Store }

func (s packageListingFails) Packages() port.PackageStore {
	return failingPackageList{s.Store.Packages()}
}

type failingPackageList struct{ port.PackageStore }

func (failingPackageList) List(
	context.Context,
	string,
	kernel.Page,
) ([]model.Package, string, error) {
	return nil, "", errFault
}
