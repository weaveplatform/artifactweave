package configuration_test

// This file holds the configuration suites' fixtures: adapters, under the
// names of the router guards' fixtures in internal/app, over
// test/server/support/harness and test/server/support, and copies of the world
// and helpers those fixtures define, which app keeps for its guard tables.
// Every router here is harness.Router, so a change to the router's assembly
// reaches these tests without an edit here.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

// New is the production router over c.
func New(c Config) http.Handler { return harness.Router(c) }

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db *inmem.Store) {
	t.Helper()
	support.Seed(t, db)
}

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func basic(user, password string) map[string]string { return support.Basic(user, password) }

func bearerAuth(token string) map[string]string { return support.Bearer(token) }

func formAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

// statusFixture is the production router over a seeded store whose admin
// password is "pw", with the local repository libs-local and a file blob
// store: what the root status fixture gives the tests here, which deploy,
// read storage info and create a Docker repository as the admin. tune
// adjusts the configuration before the router is built.
func statusFixture(t *testing.T, anonymous bool, tune ...harness.Tune) http.Handler {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	if anonymous {
		if err := db.Settings().
			Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "libs-local", Class: repo.Local,
	})); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: support.TestHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: support.TestHash,
	}
	for _, f := range tune {
		f(&cfg)
	}
	return harness.Router(cfg)
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

// repoWorld is a seeded store behind the router: admin and "reader" (in the
// readers group, so the seeded Anything permission lets it read every
// repository) both use password "pw".
type repoWorld struct {
	t  *testing.T
	h  http.Handler
	db *inmem.Store
	// fail makes the store's repository index fail the named operations.
	fail       map[string]bool
	remoteHTTP *upstream.Pool
}

func newRepoWorld(t *testing.T) *repoWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	reader := accesstest.User{
		Name:         "reader",
		PasswordHash: cheapHash,
		Groups:       []string{authn.ReadersGroup},
	}
	if err := db.Users().Create(ctx, reader); err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	w := &repoWorld{t: t, db: db, fail: map[string]bool{}}
	w.remoteHTTP = new(upstream.Pool)
	t.Cleanup(w.remoteHTTP.Close)
	w.h = New(Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: faultyStore{Store: db, fail: w.fail}, Auth: a,
		RemoteHTTP: w.remoteHTTP,
	})
	return w
}

func (w *repoWorld) create(cs ...repo.Config) {
	w.t.Helper()
	for _, c := range cs {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *repoWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	h := map[string]string{}
	if as != "" {
		h = basic(as, "pw")
	}
	if body != "" {
		h["Content-Type"] = "application/json"
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return do(w.t, w.h, method, path, h, body)
}

func (w *repoWorld) stored(key string) repo.Config {
	w.t.Helper()
	c, err := w.db.Repos().Get(context.Background(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// faultyStore fails the repository listing with errBoom while fail names
// "list", the one failure the descriptor suite asks for.
type faultyStore struct {
	*inmem.Store
	fail map[string]bool
}

var errBoom = errors.New("boom")

func (f faultyStore) Repos() port.RepoStore {
	return faultyRepos{RepoStore: f.Store.Repos(), fail: f.fail}
}

type faultyRepos struct {
	port.RepoStore
	fail map[string]bool
}

func (f faultyRepos) List(ctx context.Context, rf port.RepoFilter) ([]repo.Config, error) {
	if f.fail["list"] {
		return nil, errBoom
	}
	return f.RepoStore.List(ctx, rf) //nolint:wrapcheck // test double
}

// The router health states and the node id the system handlers write,
// copied with the same text: the health suites build the documents they
// expect from them.
const (
	stateHealthy   = "HEALTHY"
	stateUnhealthy = "UNHEALTHY"
)

var nodeID = sync.OnceValue(func() string {
	host, _ := os.Hostname()
	return host
})
