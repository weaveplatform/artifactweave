package artifacts_test

// This file holds the artifacts management suites' fixtures: adapters, under
// the names of the router guards' fixtures in migration/app, over
// test/server/support/harness and test/server/support, and copies of the
// worlds and helpers those fixtures define, which app keeps for its guard
// tables. Every router here is harness.Router, so a change to the router's
// assembly reaches these tests without an edit here.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	accessmodule "github.com/weaveplatform/artifactweave/migration/access"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	artifactshttp "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/multipart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

// New is the production router over c.
func New(c Config) http.Handler { return harness.Router(c) }

type (
	// Mirrors is the type of Config.Mirrors.
	Mirrors = harness.Mirrors
	// MirrorStatus is one tier's mirror in what Mirrors reports.
	MirrorStatus = harness.MirrorStatus
	// SearchLimits is the type of Config.Search.
	SearchLimits = harness.SearchLimits
)

// DefaultRedirectTTL is how long a signed download URL stays valid when
// Config.Redirect sets no TTL.
const DefaultRedirectTTL = harness.DefaultRedirectTTL

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
	admin     = basic("admin", "pw")
)

func cheapSeed(t *testing.T, db support.SeedStore) {
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

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	harness.WantStatus(t, rec, want)
}

func ptr[T any](v T) *T { return &v }

// itemWorld is the harness's item world under the fields the tests read.
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

// anonymousRemotes is the permission newItemWorld gives anonymous when
// anonymous access is on: read on every remote repository.
const anonymousRemotes = harness.AnonymousRemotes

func newItemWorld(t *testing.T, anonymous bool, tune ...harness.Tune) *itemWorld {
	t.Helper()
	w := harness.NewItemWorld(t, anonymous, tune...)
	return &itemWorld{t: w.T, h: w.H, db: w.DB, blobs: w.Blobs}
}

func (w *itemWorld) world() *harness.ItemWorld {
	return &harness.ItemWorld{T: w.t, H: w.h, DB: w.db, Blobs: w.blobs}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.world().Do(method, path, as, body, headers...)
}

func (w *itemWorld) item(key string, cached bool, p string) model.Item {
	w.t.Helper()
	return w.world().Item(key, cached, p)
}

// signingBlobs is harness.SigningBlobs under the field names the tests set:
// it signs the routes it lists, and err fails every signature.
type signingBlobs struct {
	blob.Store
	routes []blob.Via
	err    error
}

func storageSigner(st blob.Store) signingBlobs {
	return signingBlobs{Store: st, routes: []blob.Via{blob.ViaStorage}}
}

func (b signingBlobs) signer() harness.SigningBlobs {
	return harness.SigningBlobs{Store: b.Store, Routes: b.routes, Err: b.err}
}

func (b signingBlobs) Signs(via blob.Via) bool { return b.signer().Signs(via) }

func (b signingBlobs) SignURL(ctx context.Context, sha string, o blob.SignOptions) (string, error) {
	return b.signer().SignURL(ctx, sha, o)
}

// withAccess returns c with the access module that the router builds over
// a configuration that sets none: over c's store, authenticator, token
// service, settings and logger. reindexWorld registers its own work with
// that module's authority, as the runtime does.
func withAccess(c Config) Config {
	m, err := accessmodule.New(accessmodule.Deps{
		Store: c.Store, Authenticator: c.Auth, Tokens: c.Tokens,
		Settings: accessapi.Settings{
			AllowBasicAuthTokens:   c.AllowBasicAuthTokens,
			GlobalEnvironmentLimit: c.GlobalEnvironmentLimit,
		},
		Log: c.Logger,
	})
	if err == nil {
		c.Access = m.API()
	}
	return c
}

// newArtifactoryAPI is the artifacts partial over c's store, blob store and
// access module, with the private predicate the router gives the item kit,
// for the tests that call its operations and kit methods without the
// router.
func newArtifactoryAPI(c Config) artifactshttp.ArtifactoryAPI {
	return artifactshttp.New(artifactshttp.Deps{Store: c.Store, Deps: itemkit.Deps{
		Blobs: c.Blobs, Access: c.Access, Private: artifactshttp.PrivateItem,
	}})
}

// testHandler is the router without a store, which answers every operation
// that needs one like the stub it replaces.
func testHandler() http.Handler {
	return New(Config{
		BaseURL: "https://cfg.example.com", CompatVersion: "7.155.0",
		Revision: "v0.1.0-3-gabc",
	})
}

// target is repository c as the router's permission checks name it to the
// access module.
func target(c repo.Config) accessapi.Repo {
	return accessapi.Repo{Key: c.Key, Class: string(c.Class)}
}

// The router's paths, media types, limits and messages that the tests name,
// spelled as the router and its handlers spell them, so that a change there
// fails a test here.
const (
	buildsPath = "/artifactory/api/build"
	aqlTrimmed = "AQL query reached the search hard limit, results are trimmed. " +
		"Please address configurable limit."
	copyOrMoveType  = "application/vnd.org.jfrog.artifactory.storage.CopyOrMoveResult+json"
	redirectChoice  = "X-JFrog-Download-Redirect-To"
	requestIDHeader = "X-Request-ID"
	restoredMessage = "Successfully restored trash items"
	traceTime       = "2006-01-02T15:04:05.000Z07:00"
	repoBatchLimit  = 100
)

// searchPage is how many rows the search core reads at a time.
// TestAccSearchPages seeds one more file than that, so it reads the core's
// own size: a copy would let the page size change under it and the test
// stop crossing a page.
const searchPage = search.SearchPage

const (
	uploadsCreate   = "/artifactory/api/v1/uploads/create"
	uploadsURLPart  = "/artifactory/api/v1/uploads/urlPart"
	uploadsPart     = "/artifactory/api/v1/uploads/part"
	uploadsComplete = "/artifactory/api/v1/uploads/complete"
	uploadsStatus   = "/artifactory/api/v1/uploads/status"
	uploadsAbort    = "/artifactory/api/v1/uploads/abort"
)

const (
	mediaLocal     = "application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json"
	mediaRemote    = "application/vnd.org.jfrog.artifactory.repositories.RemoteRepositoryConfiguration+json"
	mediaVirtual   = "application/vnd.org.jfrog.artifactory.repositories.VirtualRepositoryConfiguration+json"
	mediaFederated = "application/vnd.org.jfrog.artifactory.repositories.FederatedRepositoryConfiguration+json"
	mediaDetails   = "application/vnd.org.jfrog.artifactory.repositories.RepositoryDetailsList+json"
	mediaConfigs   = "application/vnd.org.jfrog.artifactory.repositories.RepositoryConfigurationsList+json"
	mediaShared    = "application/vnd.org.jfrog.artifactory.repositories.SharedRemoteRepositoryConfiguration+json"
)

const (
	mediaFileInfo     = "application/vnd.org.jfrog.artifactory.storage.FileInfo+json"
	mediaFolderInfo   = "application/vnd.org.jfrog.artifactory.storage.FolderInfo+json"
	mediaFileList     = "application/vnd.org.jfrog.artifactory.storage.FileList+json"
	mediaLastModified = "application/vnd.org.jfrog.artifactory.storage.ItemLastModified+json"
	mediaProperties   = "application/vnd.org.jfrog.artifactory.storage.ItemProperties+json"
	mediaStats        = "application/vnd.org.jfrog.storage.StatsInfo+json"
)

const (
	mediaArtifactSearch       = "application/vnd.org.jfrog.artifactory.search.ArtifactSearchResult+json"
	mediaBadChecksumSearch    = "application/vnd.org.jfrog.artifactory.search.BadChecksumSearchResult+json"
	mediaPatternSearch        = "application/vnd.org.jfrog.artifactory.search.PatternResultFileSet+json"
	mediaUsageSearch          = "application/vnd.org.jfrog.artifactory.search.ArtifactUsageResult+json"
	mediaGavcSearch           = "application/vnd.org.jfrog.artifactory.search.GavcSearchResult+json"
	mediaVersionsSearch       = "application/vnd.org.jfrog.artifactory.search.ArtifactVersionsResult+json"
	mediaDependencySearch     = "application/vnd.org.jfrog.artifactory.search.DependencyBuilds+json"
	mediaBuildArtifactsSearch = "application/vnd.org.jfrog.artifactory.search.BuildArtifactsSearchResult+json"
)

const (
	msgKeyExists        = "Case insensitive repository key already exists"
	msgProtected        = "Cannot delete repository: repository key is blank, trash or support bundle"
	msgAllRemoved       = "All repositories were removed successfully"
	noVersions          = "Unable to find artifact versions"
	noLatestRelease     = "Latest release version not found"
	noLatestIntegration = "Latest integration version not found"
)

// statusCase is one request and the status it must get, as the root's
// status table writes it.
type statusCase struct {
	// route is the operation as the implemented map names it.
	route string
	// path is the request path, query included.
	path string
	// as is who calls: "" without credentials (anonymous access off),
	// "anonymous" without credentials (anonymous access on), "admin" or
	// "user" (a user in no group).
	as       string
	body     string
	expected int
}

func (c statusCase) method() string { m, _, _ := strings.Cut(c.route, " "); return m }

// statusFixture is the world of the root status fixture behind the
// production router: a seeded store with the admin (password "pw"), a user
// "user" (password "pw") in no group and a locked user "locked"; the
// repositories libs-local, remote-shared, remote-private, virt, del-one,
// del-batch and upd-batch; the files fixture/get.txt, fixture/del.txt and
// fixture/move.txt, each holding "fixture", and fixture/archive.zip in
// libs-local; folder download on; a token service; and the multipart upload
// service. It leaves out what only the format, build, search, overwrite,
// key pair, permission and token rows need, which no test here reaches.
func statusFixture(t *testing.T, anonymous bool, tune ...func(*Config)) http.Handler {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Users().
		Create(ctx, model.User{Name: "user", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(ctx, model.User{
		Name: "locked", PasswordHash: cheapHash, Status: model.UserLocked,
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		if err := db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	shared := true
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "remote-shared", Class: repo.Remote, URL: "https://up.example", ShareConfiguration: &shared},
		{Key: "remote-private", Class: repo.Remote, URL: "https://up.example"},
		{Key: "virt", Class: repo.Virtual, Repositories: []string{"libs-local"}},
		{Key: "del-one", Class: repo.Local},
		{Key: "del-batch", Class: repo.Local},
		{Key: "upd-batch", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Formats: ecosystem.For}
	libs, err := db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fixture/get.txt", "fixture/del.txt", "fixture/move.txt"} {
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: libs, Path: p}, Body: strings.NewReader("fixture"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: libs, Path: "fixture/archive.zip"},
		Body:   bytes.NewReader(zipBytes(t, map[string]string{"inner.txt": "inner"})),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Settings().Put(ctx, settings.SettingFolderDownload,
		model.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: testHash,
		Tokens: tokens,
		Multipart: &multipart.Service{
			Store: db, Blobs: blobs, Dir: t.TempDir(), Restrictions: restrictionsOver(t, db),
		},
	}
	for _, f := range tune {
		f(&cfg)
	}
	return New(cfg)
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
	reader := model.User{
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

// identityWorld is a seeded store (admin "pw"), a token service, and a server
// that hashes cheaply.
type identityWorld struct {
	h      http.Handler
	db     *inmem.Store
	tokens *token.Service
}

func newIdentityWorld(t *testing.T) identityWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Instance().Init(ctx, instance.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: testHash, Tokens: tokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{CompatVersion: "7.155.0", Store: db, Auth: a, PasswordHash: testHash})
	return identityWorld{h: h, db: db, tokens: tokens}
}

func (w identityWorld) bearer(t *testing.T, user string, scope string) map[string]string {
	t.Helper()
	req := token.Request{Username: user, ByAdmin: true}
	if scope != "" {
		sc, err := kernel.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		req.Scope = sc
	}
	issued, err := w.tokens.Issue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + issued.AccessToken}
}

// testStarted is a started timestamp in the layout the JFrog CLI writes.
const testStarted = "2026-10-15T09:30:00.000+0000"

// allowBuilds lets a fixture publish builds without a caller.
type allowBuilds struct{}

func (allowBuilds) Build(string, string, bool, accessapi.Actions) bool { return true }
func (allowBuilds) Item(repo.Config, string, accessapi.Actions) bool   { return true }

// buildDoc is a build-info document of one run without modules.
func buildDoc(name, number, started string) string {
	return fmt.Sprintf(`{"name":%q,"number":%q,"started":%q}`, name, number, started)
}

// p8Types are the package types whose reindex operations P8 serves.
var p8Types = []repo.PackageType{repo.PyPI, repo.NuGet, repo.Helm, repo.Terraform}

// pkgtestProtocol is the test-only protocol that drives the router kit
// through the whole server: authentication, the item routes' download and
// the item service's deploy. It serves generic repositories, so the
// fixtures' libs-local and its grants are its repository.
func pkgtestProtocol() pkgroute.Protocol {
	files := func(k *pkgroute.Kit, w http.ResponseWriter, r *http.Request) (pkgroute.Path, bool) {
		p, err := pkgroute.ParsePath(strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "files/"))
		if err != nil {
			k.Error(w, r, err)
			return p, false
		}
		return p, true
	}
	return pkgroute.Protocol{
		Name:         "pkgtest",
		Types:        []repo.PackageType{repo.Generic},
		ErrorProfile: apierror.ProfileArtifactory,
		Reserved:     []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			serve := func(w http.ResponseWriter, r *http.Request) {
				if p, ok := files(k, w, r); ok {
					k.ServeItem(w, r, p.String())
				}
			}
			r.Get("/files/*", serve)
			r.Head("/files/*", serve)
			r.Put("/files/*", func(w http.ResponseWriter, r *http.Request) {
				if p, ok := files(k, w, r); ok {
					k.Deploy(w, r, p.String(), p.Props)
				}
			})
		},
	}
}

// loadSpec loads and validates the bundled service spec api/<name>.
func loadSpec(t *testing.T, name string) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(harness.RepoFile(t, "api/"+name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate %s: %v", name, err)
	}
	return doc
}

const npmBase = "/artifactory/api/npm/"

func npmPublishBody(name, version string) string { return support.NPMPublishBody(name, version) }
func npmTarballOf(name, version string) []byte   { return support.NPMTarballOf(name, version) }

func helmChart(name, version string) []byte { return support.HelmChart(name, version) }

func sha256Digest(body string) string { return support.SHA256Digest(body) }

const registryConfig = support.RegistryConfig

var registryLayer = support.RegistryLayer

func registryManifest(note string) string { return support.RegistryManifest(note) }
