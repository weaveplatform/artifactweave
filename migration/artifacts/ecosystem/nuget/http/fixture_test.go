package nugethttp_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	nugetfmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/nuget"
	nugethttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/nuget/http"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

const (
	// base is the fixture's base URL, which every URL a response carries
	// starts with.
	base = "https://nuget.example"
	v3   = "/artifactory/api/nuget/v3/nuget-local"
)

// fixture is the NuGet protocols mounted as the server mounts them, over a
// store whose nuget-local holds what each test deploys, and nuget-forced,
// a local repository with forceNugetAuthentication on. A request names its
// principal in X-Test-User: none, anonymous, admin, or a user, whose
// grants on nuget-local, and on nuget-forced alike, the fixture gives:
//   - reader reads all;
//   - partial reads only the 1.x packages of Acme.Mixed;
//   - writer reads, deploys, deletes and annotates all;
//   - deployer reads and deploys all, and deletes nothing;
//   - teamwriter reads and deploys under team/ alone;
//   - pkgdeleter reads all and deletes the .nupkg files at the root;
//   - nobody holds nothing.
//
// serverStore is the one store the fixture's item service, router kit,
// access kit and remote setup share, which a fault store can replace.
type serverStore interface {
	item.Store
	pkgroute.Store
	accesstest.Store
}

type fixture struct {
	t     *testing.T
	h     http.Handler
	db    serverStore
	blobs blob.Store
	items *item.Service
	// mu guards tick and faults, which concurrent pushes reach.
	mu     sync.Mutex
	tick   time.Time
	faults []error
}

func newFixture(t *testing.T, wrap ...func(pkgroute.Store) pkgroute.Store) *fixture {
	t.Helper()
	return newFixtureOn(t, inmem.New(), wrap...)
}

func newFixtureOn(
	t *testing.T,
	db serverStore,
	wrap ...func(pkgroute.Store) pkgroute.Store,
) *fixture {
	t.Helper()
	ctx := context.Background()
	force := true
	for _, c := range []repo.Config{
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{
			Key: "nuget-forced", Class: repo.Local, PackageType: repo.NuGet,
			ForceNugetAuthentication: &force,
		},
		{Key: "nuget-remote", Class: repo.Remote, PackageType: repo.NuGet, URL: "https://up.example"},
		{Key: "libs-local", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	permit := func(name, user, include string, actions api.Actions) {
		t.Helper()
		target := model.Target{Include: []string{include}}
		if err := db.Permissions().Create(ctx, model.Permission{
			Name: name,
			Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
				Actions: model.Grants{Users: map[string]api.Actions{user: actions}},
				Targets: map[string]model.Target{"nuget-local": target, "nuget-forced": target},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read, write, del := api.ActionRead, api.ActionWrite, api.ActionDelete
	permit("reader", "reader", "**", read)
	permit("partial", "partial", "Acme.Mixed.1.*", read)
	permit("writer", "writer", "**", read|write|del|api.ActionAnnotate)
	permit("deployer", "deployer", "**", read|write)
	permit("teamwriter", "teamwriter", "team/**", read|write)
	permit("pkgdeleter-read", "pkgdeleter", "**", read)
	permit("pkgdeleter-delete", "pkgdeleter", "*.nupkg", del)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, blobs: blobs, tick: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	f.items = &item.Service{Store: db, Blobs: blobs, Now: f.now, Formats: ecosystem.For}
	var backend pkgroute.Store = db
	for _, w := range wrap {
		backend = w(backend)
	}
	o := pkgroute.Options{
		Store: backend,
		Items: func() *item.Service { return f.items },
		// The item routes' download, reduced to the content.
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it model.Item) {
			rc, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
			if err != nil {
				t.Errorf("open %s: %v", it.FullPath(), err)
				return
			}
			defer func() { _ = rc.Close() }()
			w.Header().Set("X-Served", it.FullPath())
			_, _ = io.Copy(w, rc)
		},
		ItemError: itemError,
		Fault: func(_ *http.Request, err error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.faults = append(f.faults, err)
		},
	}
	authorize := accesstest.Over(t, db).Middleware(t, true, func(r *http.Request) kernel.Principal {
		name := r.Header.Get("X-Test-User")
		if name == kernel.AnonymousUser {
			return kernel.Anonymous
		}
		return kernel.Principal{Name: name, Admin: name == "admin"}
	})
	principal := func(next http.Handler) http.Handler {
		authorized := authorize(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-User") == "" {
				next.ServeHTTP(w, r)
				return
			}
			authorized.ServeHTTP(w, r)
		})
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(base), principal)
	pkgroute.Mount(r, o, nugethttp.V3())
	pkgroute.Mount(r, o, nugethttp.V2())
	f.h = r
	return f
}

// itemError maps the item service's refusals as the item kit's ItemError
// does, for those these tests meet.
func itemError(_ context.Context, _ string, err error) error {
	var refused *format.RejectionError
	if errors.As(err, &refused) {
		return apierror.New(refused.Status, refused.Message)
	}
	return err
}

// now is a clock a minute later at each write, so each version has its
// own time.
func (f *fixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick = f.tick.Add(time.Minute)
	return f.tick
}

// deploy stores body at path in nuget-local through the item service, so
// the NuGet format checks and indexes it.
func (f *fixture) deploy(path string, body []byte) model.Item {
	f.t.Helper()
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, "nuget-local")
	if err != nil {
		f.t.Fatal(err)
	}
	it, err := f.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(body),
	})
	if err != nil {
		f.t.Fatalf("deploy %s: %v", path, err)
	}
	return it
}

// push deploys the package of id at version, with more nuspec elements, at
// its canonical path at the repository root, and returns its bytes.
func (f *fixture) push(id, version, more string) []byte {
	f.t.Helper()
	body := nupkg(id, version, more)
	v, ok := nugetfmt.ParseVersion(version)
	if !ok {
		f.t.Fatalf("%s is no version", version)
	}
	f.deploy(nugetfmt.FileName(id, v), body)
	return body
}

// do makes a request as user; headers are name, value pairs.
func (f *fixture) do(method, path, user string, headers ...string) *httptest.ResponseRecorder {
	return f.doBody(method, path, user, nil, headers...)
}

func (f *fixture) doBody(
	method, path, user string,
	body io.Reader,
	headers ...string,
) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// zipEntry is an entry of a test zip.
type zipEntry struct{ name, body string }

// zipOf is entries as a zip, stored without compression so the same
// entries always give the same bytes.
func zipOf(entries ...zipEntry) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		_, _ = io.WriteString(w, e.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// nuspecXML is a .nuspec of id at version, with more elements in its
// <metadata>.
func nuspecXML(id, version, more string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">` +
		`<metadata><id>` + id + `</id><version>` + version + `</version>` +
		`<authors>Acme</authors><description>The ` + id + ` package</description>` + more +
		`</metadata></package>`
}

// nupkg is a package of id at version, with more nuspec elements, as dotnet
// pack lays it out.
func nupkg(id, version, more string) []byte {
	return zipOf(
		zipEntry{"_rels/.rels", "<Relationships/>"},
		zipEntry{id + ".nuspec", nuspecXML(id, version, more)},
		zipEntry{"lib/net8.0/" + id + ".dll", "MZ " + version},
		zipEntry{"[Content_Types].xml", "<Types/>"},
	)
}

// snupkg is the symbols package of id at version.
func snupkg(id, version string) []byte {
	return zipOf(
		zipEntry{id + ".nuspec", nuspecXML(id, version,
			`<packageTypes><packageType name="SymbolsPackage" /></packageTypes>`)},
		zipEntry{"lib/net8.0/" + id + ".pdb", "PDB " + version},
	)
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body, err)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

// relative is u, an absolute URL a response carried, as a path the fixture
// serves; it fails on a URL under any other base.
func relative(t *testing.T, u string) string {
	t.Helper()
	p, ok := strings.CutPrefix(u, base)
	if !ok {
		t.Fatalf("%s is not under %s", u, base)
	}
	return p
}

var errBroken = errors.New("broken store")

// failingStore fails one of its package index's reads, or returns
// versions whose metadata does not decode.
type failingStore struct {
	pkgroute.Store
	failing string
}

func (s failingStore) Leases() work.LeaseStore {
	return failingLeases{LeaseStore: s.Store.Leases(), failing: s.failing}
}

type failingLeases struct {
	work.LeaseStore
	failing string
}

func (l failingLeases) Acquire(
	ctx context.Context,
	lease work.Lease,
	now time.Time,
) (bool, error) {
	if l.failing == "lease" {
		return false, errBroken
	}
	return l.LeaseStore.Acquire(ctx, lease, now) //nolint:wrapcheck // a test's pass-through
}

func (s failingStore) Items() port.ItemStore {
	return failingItems{ItemStore: s.Store.Items(), failing: s.failing}
}

type failingItems struct {
	port.ItemStore
	failing string
}

func (i failingItems) Get(
	ctx context.Context,
	repoKey string,
	cached bool,
	path string,
) (model.Item, error) {
	if i.failing == "item" {
		return model.Item{}, errBroken
	}
	return i.ItemStore.Get(ctx, repoKey, cached, path) //nolint:wrapcheck // a test's pass-through
}

func (i failingItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if i.failing == "byid" {
		return model.Item{}, errBroken
	}
	return i.ItemStore.GetByID(ctx, id) //nolint:wrapcheck // a test's pass-through
}

func (i failingItems) Stats(ctx context.Context, id int64) (model.ItemStats, error) {
	if i.failing == "stats" {
		return model.ItemStats{}, errBroken
	}
	return i.ItemStore.Stats(ctx, id) //nolint:wrapcheck // a test's pass-through
}

func (s failingStore) Packages() port.PackageStore {
	return failingPackages{PackageStore: s.Store.Packages(), failing: s.failing}
}

type failingPackages struct {
	port.PackageStore
	failing string
}

func (p failingPackages) Find(ctx context.Context, key, name string) (model.Package, error) {
	if p.failing == "find" {
		return model.Package{}, errBroken
	}
	return p.PackageStore.Find(ctx, key, name) //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) List(
	ctx context.Context,
	key string,
	page kernel.Page,
) ([]model.Package, string, error) {
	if p.failing == "list" {
		return nil, "", errBroken
	}
	return p.PackageStore.List(ctx, key, page) //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
	vs, err := p.PackageStore.Versions(ctx, id)
	switch p.failing {
	case "versions":
		return nil, errBroken
	case "metadata":
		for i := range vs {
			vs[i].Metadata = []byte("{")
		}
	}
	return vs, err //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) Files(ctx context.Context, versionID int64) ([]int64, error) {
	if p.failing == "files" {
		return nil, errBroken
	}
	return p.PackageStore.Files(ctx, versionID) //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) Version(
	ctx context.Context,
	id int64,
	version string,
) (model.PackageVersion, error) {
	v, err := p.PackageStore.Version(ctx, id, version)
	switch p.failing {
	case "version":
		return model.PackageVersion{}, errBroken
	case "metadata":
		v.Metadata = []byte("{")
	}
	return v, err //nolint:wrapcheck // a test's pass-through
}
