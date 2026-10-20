package nuget_test

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
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// base is the fixture's base URL, which every URL a response carries
	// starts with.
	base = "https://nuget.example"
	v3   = "/artifactory/api/nuget/v3/nuget-local"
)

// fixture is the NuGet protocols mounted as the server mounts them, over a
// store whose nuget-local holds what each test deploys. A request names its
// principal in X-Test-User: none, anonymous, admin, or a user, whose
// grants the fixture gives:
//   - reader reads all of nuget-local;
//   - partial reads only the 1.x packages of Acme.Mixed;
//   - writer reads, deploys, deletes and annotates all of nuget-local;
//   - deployer reads and deploys all of nuget-local, and deletes nothing;
//   - nobody holds nothing.
type fixture struct {
	t      *testing.T
	h      http.Handler
	db     store.Store
	blobs  blob.Store
	items  *item.Service
	tick   time.Time
	faults []error
}

func newFixture(t *testing.T, wrap ...func(store.Store) store.Store) *fixture {
	t.Helper()
	return newFixtureOn(t, inmem.New(), wrap...)
}

func newFixtureOn(t *testing.T, db store.Store, wrap ...func(store.Store) store.Store) *fixture {
	t.Helper()
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "nuget-local", Class: repo.Local, PackageType: repo.NuGet},
		{Key: "nuget-remote", Class: repo.Remote, PackageType: repo.NuGet, URL: "https://up.example"},
		{Key: "libs-local", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(user, include string, actions types.Actions) {
		t.Helper()
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: user,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: actions}},
				Targets: map[string]types.Target{"nuget-local": {Include: []string{include}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read, write, del := types.ActionRead, types.ActionWrite, types.ActionDelete
	grant("reader", "**", read)
	grant("partial", "Acme.Mixed.1.*", read)
	grant("writer", "**", read|write|del|types.ActionAnnotate)
	grant("deployer", "**", read|write)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, blobs: blobs, tick: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	f.items = &item.Service{Store: db, Blobs: blobs, Now: f.now}
	backend := db
	for _, w := range wrap {
		backend = w(backend)
	}
	o := pkgroute.Options{
		Store: backend,
		Items: func() *item.Service { return f.items },
		// The item routes' download, reduced to the content.
		ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
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
		Fault:     func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
	principal := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Test-User")
			if name == "" {
				next.ServeHTTP(w, r)
				return
			}
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == auth.AnonymousUser {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(base), principal)
	pkgroute.Mount(r, o, nuget.V3())
	pkgroute.Mount(r, o, nuget.V2())
	f.h = r
	return f
}

// itemError maps the item service's refusals as the server's itemError
// does, for those these tests meet.
func itemError(_ context.Context, _ string, err error) error {
	var refused *format.Rejection
	if errors.As(err, &refused) {
		return apierror.New(refused.Status, refused.Message)
	}
	return err
}

// now is a clock a minute later at each write, so each version has its
// own time.
func (f *fixture) now() time.Time {
	f.tick = f.tick.Add(time.Minute)
	return f.tick
}

// deploy stores body at path in nuget-local through the item service, so
// the NuGet format checks and indexes it.
func (f *fixture) deploy(path string, body []byte) types.Item {
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
	store.Store
	failing string
}

func (s failingStore) Packages() store.PackageStore {
	return failingPackages{PackageStore: s.Store.Packages(), failing: s.failing}
}

type failingPackages struct {
	store.PackageStore
	failing string
}

func (p failingPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.failing == "find" {
		return types.Package{}, errBroken
	}
	return p.PackageStore.Find(ctx, key, name) //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) List(
	ctx context.Context,
	key string,
	page store.Page,
) ([]types.Package, string, error) {
	if p.failing == "list" {
		return nil, "", errBroken
	}
	return p.PackageStore.List(ctx, key, page) //nolint:wrapcheck // a test's pass-through
}

func (p failingPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
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

func (p failingPackages) Version(
	ctx context.Context,
	id int64,
	version string,
) (types.PackageVersion, error) {
	v, err := p.PackageStore.Version(ctx, id, version)
	switch p.failing {
	case "version":
		return types.PackageVersion{}, errBroken
	case "metadata":
		v.Metadata = []byte("{")
	}
	return v, err //nolint:wrapcheck // a test's pass-through
}
