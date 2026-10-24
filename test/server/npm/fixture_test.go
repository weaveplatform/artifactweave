package npm_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/multipart"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func cheapSeed(t *testing.T, db store.Store) { t.Helper(); support.Seed(t, db) }

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
func bearerAuth(token string) map[string]string     { return support.Bearer(token) }
func contentTypeOf(body string) (string, string)    { return "application/json", body }

func fixtureRepos(name string, kind repo.PackageType) []repo.Config {
	local := name + "-local"
	return []repo.Config{
		{Key: local, Class: repo.Local, PackageType: kind},
		{Key: name + "-remote", Class: repo.Remote, PackageType: kind, URL: "https://up.example"},
		{
			Key: name + "-virtual", Class: repo.Virtual, PackageType: kind,
			Repositories: []string{local}, DefaultDeploymentRepo: local,
		},
	}
}

// npmServer assembles the same real router as the root tests. Tune runs
// before construction so package fixtures can add permissions and work.
func npmServer(
	t *testing.T,
	tune ...func(*server.Config),
) (http.Handler, *inmem.Store, *token.Service) {
	t.Helper()
	ctx := t.Context()
	db := inmem.New()
	cheapSeed(t, db)
	for _, name := range []string{"dev", "user"} {
		if err := db.Users().
			Create(ctx, types.User{Name: name, PasswordHash: cheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range npmRepos[:1] {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(
		db.Users(),
		db.Settings(),
		auth.Options{Hash: testHash, Tokens: tokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: blobs, Auth: a, Tokens: tokens, PasswordHash: testHash,
		Multipart: &multipart.Service{Store: db, Blobs: blobs, Dir: t.TempDir()},
	}
	for _, f := range tune {
		f(&cfg)
	}
	return server.New(cfg), db, tokens
}

type statusPart struct {
	handwritten, generated map[string]bool
	rows                   func() []statusCase
	seed                   func(*testing.T, store.Store, blob.Store)
}

type statusCase struct {
	route, path, as, body string
	want                  int
}

func (c statusCase) method() string { m, _, _ := strings.Cut(c.route, " "); return m }

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func newItemWorld(t *testing.T, anonymous bool, tune ...func(*server.Config)) *itemWorld {
	t.Helper()
	var blobs blob.Store
	h, db, _ := npmServer(t, func(cfg *server.Config) {
		for _, name := range []string{"deployer"} {
			if err := cfg.Store.Users().
				Create(t.Context(), types.User{Name: name, PasswordHash: cheapHash}); err != nil {
				t.Fatal(err)
			}
		}
		if err := cfg.Store.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})); err != nil {
			t.Fatal(err)
		}
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range tune {
			f(cfg)
		}
		blobs = cfg.Blobs
	})
	return &itemWorld{t: t, db: db, blobs: blobs, h: h}
}

type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

func (w *itemWorld) do(method, path, as, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	headers := map[string]string{}
	if as != "" {
		headers = basic(as, "pw")
	}
	return do(w.t, w.h, method, path, headers, body)
}

// A route inventory must be enforced both where registration is checked and
// where the independently runnable npm assertions live.
func TestAccNPMRouteTablesCoverImplemented(t *testing.T) {
	routes := support.NPMRoutes()
	status, matrix := map[string]bool{}, map[string]bool{}
	for _, part := range []statusPart{npmStatusPart, npmAuthStatusPart, npmLifecycleStatusPart} {
		for _, row := range part.rows() {
			status[row.route] = true
		}
	}
	for _, part := range []matrixPart{npmMatrixPart, npmAuthMatrixPart, npmLifecycleMatrixPart} {
		for _, row := range part.rows() {
			matrix[row.route] = true
		}
	}
	for route := range routes {
		if !status[route] {
			t.Errorf("%s has no npm status row", route)
		}
		if !matrix[route] {
			t.Errorf("%s has no npm principal row", route)
		}
	}
	for route := range status {
		if !routes[route] && route != npmDeploy && route != npmWebLogin {
			t.Errorf("%s is an unregistered npm status row", route)
		}
	}
	for route := range matrix {
		if !routes[route] {
			t.Errorf("%s is an unregistered npm principal row", route)
		}
	}
}

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func decodeObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
