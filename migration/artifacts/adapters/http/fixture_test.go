package artifactshttp

// This file holds the fixtures of the tests that call the partial and its
// helpers without the router, under the names they had in internal/server.
// A test here cannot build the router, which imports this package, so the
// worlds are the stores the router's worlds hold, and the access module is
// accesstest's over them. The route suites under test/server/artifacts
// reach the rest through the router.

import (
	"context"
	"crypto/md5"  //nolint:gosec // MD5 is a wire checksum here
	"crypto/sha1" //nolint:gosec // SHA-1 is a wire checksum here
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Config is the part of the router's configuration that the tests here
// set, which newArtifactoryAPI passes on as the router does.
type Config struct {
	Store  store.Store
	Blobs  blob.Store
	Access accessapi.API
	AQL    AQLLimits
	GC     *gc.Collector
}

// newArtifactoryAPI is the partial over c, with the private predicate the
// router gives the item kit. The router's overlays are left out: no test
// here serves an item GET.
func newArtifactoryAPI(c Config) ArtifactoryAPI {
	return New(Deps{
		Deps: itemkit.Deps{
			Store: c.Store, Blobs: c.Blobs, Access: c.Access, Private: PrivateItem,
		},
		AQL: c.AQL,
		GC:  c.GC,
	})
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
func authorizerOver(t *testing.T, st store.Store) accessapi.Authorizer {
	t.Helper()
	return accesstest.Over(t, st).API().Authorizer
}

// cheapSeed seeds db with the built-in principals, admin's password "pw".
func cheapSeed(t *testing.T, db store.Store) {
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

func basic(user, password string) map[string]string {
	return support.Basic(user, password)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

func sha1Hex(s string) string {
	b := sha1.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

func sha256Hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func md5Hex(s string) string {
	b := md5.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

// The router's path and the build start time that the tests name, spelled
// as the root's tests spell them.
const (
	aqlURL      = "/artifactory/api/search/aql"
	testStarted = "2026-10-15T09:30:00.000+0000"
)

var errBoom = errors.New("boom")

// itemWorld is the store and blob store the router's item world serves,
// for the tests that call the partial over them.
type itemWorld struct {
	t     *testing.T
	db    *inmem.Store
	blobs blob.Store
}

// newItemWorld seeds a fresh store with the built-in principals, every
// password "pw", beside a blob store on disk; anonymous turns anonymous
// access on.
func newItemWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	db := inmem.New()
	cheapSeed(t, db)
	if anonymous {
		if err := db.Settings().Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &itemWorld{t: t, db: db, blobs: blobs}
}

// publish publishes doc as admin through UploadBuild, which reads the raw
// body that the router's request details keep for it, and fails the test
// unless the publish answers 204, as a PUT of doc through the router must.
func (w *itemWorld) publish(doc string) {
	w.t.Helper()
	k := accesstest.Over(w.t, w.db)
	p, err := k.API().Authenticator.Basic(w.t.Context(), "admin", "pw")
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, err := k.Authorize(w.t.Context(), p, false)
	if err != nil {
		w.t.Fatal(err)
	}
	s := newArtifactoryAPI(Config{Store: w.db, Blobs: w.blobs, Access: k.API()})
	resp, err := s.UploadBuild(
		httpkit.WithRawBody(ctx, []byte(doc)),
		artgen.UploadBuildRequestObject{},
	)
	if err != nil {
		w.t.Fatalf("publish: %v", err)
	}
	if _, ok := resp.(artgen.UploadBuild204Response); !ok {
		w.t.Fatalf("publish answered %T, want 204", resp)
	}
}

// artifactDoc is a build-info document whose one module produced content,
// with fields the spec does not list, which must survive.
func artifactDoc(name, number, started, content string) string {
	return fmt.Sprintf(`{"name":%q,"number":%q,"started":%q,`+
		`"agent":{"name":"jfrog-cli-go","version":"2.80.0"},"vcs":[{"revision":"abc"}],`+
		`"properties":{"buildInfo.env.CI":"true","count":3},`+
		`"modules":[{"id":"m1","type":"generic","artifacts":[{"name":"a.bin","path":"app/a.bin",`+
		`"sha1":%q,"sha256":%q,"md5":%q,"localPath":"/tmp/a.bin"}]}]}`,
		name, number, started, sha1Hex(content), sha256Hex(content), md5Hex(content))
}

// buildsAQLWorld is the item routes, AQL among them, behind the access
// module's evaluation of Basic credentials, over a store with runs app/1,
// app/2 and tool/1, each producing libs-local/app.jar, and a promotion of
// app/1; dev may read libs-local and the builds under app/**. tune adjusts
// the configuration before the partial and the access module are built
// over it.
func buildsAQLWorld(t *testing.T, tune ...func(*Config)) (http.Handler, *accesstest.Kit) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(ctx, types.User{Name: "dev", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	dev := types.Grants{Users: map[string]types.Actions{"dev": types.ActionRead}}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "dev-app", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: dev,
				Targets: map[string]types.Target{"libs-local": {Include: []string{"**"}}},
			},
			types.ResourceBuild: {
				Actions: dev,
				Targets: map[string]types.Target{
					"artifactory-build-info": {Include: []string{"app/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{
		Store: db, Blobs: blobs, Formats: ecosystem.For,
	}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: "app.jar"}, Body: strings.NewReader("jar"),
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jar := types.BuildFile{Name: "app.jar", Checksums: types.Checksums{SHA1: sha1Hex("jar")}}
	for n, run := range [][2]string{{"app", "1"}, {"app", "2"}, {"tool", "1"}} {
		b, err := db.Builds().Put(ctx, types.Build{
			Name: run[0], Number: run[1], Started: base.Add(time.Duration(n) * time.Minute),
			Created: base.Add(time.Hour), CreatedBy: "ci", URL: "https://ci/" + run[0],
			Modules: []types.BuildModule{{ID: run[0], Artifacts: []types.BuildFile{jar}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if _, err := db.Builds().AddPromotion(ctx, types.BuildPromotion{
				BuildID: b.ID, Created: base.Add(2 * time.Hour), CreatedBy: "admin",
				Status: "released", Repo: "libs-release", CIUser: "jenkins",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := Config{Store: db, Blobs: blobs}
	for _, f := range tune {
		f(&cfg)
	}
	k := accesstest.Over(t, cfg.Store)
	cfg.Access = k.API()
	mux := chi.NewRouter()
	mux.Use(k.Middleware(t, false, func(r *http.Request) kernel.Principal {
		user, password, _ := r.BasicAuth()
		p, err := k.API().Authenticator.Basic(r.Context(), user, password)
		if err != nil {
			t.Errorf("basic credentials of %q: %v", user, err)
		}
		return p
	}))
	newArtifactoryAPI(cfg).RegisterItemRoutes(mux)
	return mux, k
}

// faultyStore fails the repository reads named in fail with errBoom: every
// Get under "get", or one key's under "get:<key>".
type faultyStore struct {
	*inmem.Store
	fail map[string]bool
}

func (f faultyStore) Repos() store.RepoStore {
	return faultyRepos{RepoStore: f.Store.Repos(), fail: f.fail}
}

type faultyRepos struct {
	store.RepoStore
	fail map[string]bool
}

func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail["get"] || f.fail["get:"+key] {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

// buildSearchFaults fails the item store's checksum search with errBoom
// when fail is "find".
type buildSearchFaults struct {
	store.Store
	fail string
}

func (f buildSearchFaults) Items() store.ItemStore {
	return faultyChecksumSearch{f.Store.Items(), f.fail}
}

type faultyChecksumSearch struct {
	store.ItemStore
	fail string
}

func (f faultyChecksumSearch) FindByChecksum(
	ctx context.Context,
	c types.Checksums,
	p store.Page,
) ([]types.Item, string, error) {
	if f.fail == "find" {
		return nil, "", errBoom
	}
	return f.ItemStore.FindByChecksum(ctx, c, p) //nolint:wrapcheck // test double
}
