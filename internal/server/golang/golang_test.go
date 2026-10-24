package golang_test

import (
	"context"
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
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/golang"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	base   = "/artifactory/api/go/"
	local  = "go-local"
	lib    = "example.com/acme/lib"
	pseudo = "v0.0.0-20261016120000-abcdefabcdef"
)

// created is when the fixture stores every file, so a synthesised .info's
// time is known.
var created = time.Date(2026, 10, 16, 12, 30, 45, 0, time.UTC)

// fixture is the Go protocol mounted as the server mounts it, over a store
// whose go-local holds:
//   - example.com/acme/lib: v1.0.0 (.zip, .mod), v1.1.0 (.zip, .mod and a
//     stored .info), v1.2.0-rc.1 (.zip, .mod), a pseudo-version (.zip,
//     .mod), and v1.3.0, whose .mod was never deployed;
//   - example.com/acme/pre: v1.0.0-rc.1 and a pseudo-version;
//   - example.com/acme/pseudo: a pseudo-version alone;
//   - example.com/Acme/upper: v1.0.0, a path the go command case-encodes.
//
// A request names its principal in X-Test-User. reader reads everything,
// zips reads only .zip files, infos only .info files, and nobody nothing.
type fixture struct {
	h      http.Handler
	db     store.Store
	served []string
	faults []error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	return newFixtureOn(t, db, db)
}

// newFixtureOn seeds seed and serves from serve, which may wrap seed.
func newFixtureOn(t *testing.T, seed *inmem.Store, serve store.Store) *fixture {
	t.Helper()
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: local, Class: repo.Local, PackageType: repo.Go},
		{Key: "go-fed", Class: repo.Federated, PackageType: repo.Go},
		{Key: "go-remote", Class: repo.Remote, PackageType: repo.Go, URL: "https://up.example"},
		{Key: "go-virtual", Class: repo.Virtual, PackageType: repo.Go, Repositories: []string{local}},
		{Key: "gen-local", Class: repo.Local},
	} {
		if err := seed.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(user, include string) {
		t.Helper()
		if err := seed.Permissions().Create(ctx, types.Permission{
			Name: user,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{user: types.ActionRead}},
				Targets: map[string]types.Target{
					local: {Include: []string{include}}, "go-fed": {Include: []string{include}},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant("reader", "**")
	grant("zips", "**/*.zip")
	grant("infos", "**/*.info")
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: seed, Blobs: blobs, Now: func() time.Time { return created }}
	deploy := func(key, module, version string, exts ...string) {
		t.Helper()
		c, err := seed.Repos().Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		for _, ext := range exts {
			body := module + " " + version + " " + ext
			if ext == "info" {
				body = `{"Version":"` + version + `","Time":"2020-01-02T03:04:05Z"}`
			}
			if _, err := items.Deploy(ctx, item.Deploy{
				Target: item.Target{Repo: c, Path: module + "/@v/" + version + "." + ext},
				Body:   strings.NewReader(body),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	deploy(local, lib, "v1.0.0", "zip", "mod")
	deploy(local, lib, "v1.1.0", "zip", "mod", "info")
	deploy(local, lib, "v1.2.0-rc.1", "zip", "mod")
	deploy(local, lib, pseudo, "zip", "mod")
	deploy(local, lib, "v1.3.0", "zip")
	deploy(local, "example.com/acme/pre", "v1.0.0-rc.1", "zip", "mod")
	deploy(local, "example.com/acme/pre", pseudo, "zip", "mod")
	deploy(local, "example.com/acme/pseudo", pseudo, "zip", "mod")
	deploy(local, "example.com/Acme/upper", "v1.0.0", "zip", "mod")
	deploy("go-fed", lib, "v1.0.0", "zip", "mod")

	f := &fixture{db: serve}
	o := pkgroute.Options{
		Store: serve,
		Items: func() *item.Service { return items },
		ServeItem: func(w http.ResponseWriter, _ *http.Request, c repo.Config, it types.Item) {
			f.served = append(f.served, c.Key+"/"+it.FullPath())
			_, _ = io.WriteString(w, "served "+it.FullPath())
		},
		ItemError: func(context.Context, string, error) error {
			return apierror.New(http.StatusInternalServerError, "internal error")
		},
		Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-Test-User")
			if name == "" {
				next.ServeHTTP(w, r)
				return
			}
			pr := auth.Principal{Name: name}
			if name == auth.AnonymousUser {
				pr = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: seed.Permissions(), Groups: seed.Groups(), AnonymousEnabled: true,
			}, pr)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(r, o, golang.Protocol())
	f.h = r
	return f
}

func (f *fixture) do(method, path, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// check is one request and what it must answer: the status, and a body
// that contains want, or equals the rest of it when want starts with "=".
type check struct {
	name, method, path, user string
	status                   int
	want                     string
}

func (f *fixture) run(t *testing.T, checks []check) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			method := c.method
			if method == "" {
				method = http.MethodGet
			}
			rec := f.do(method, c.path, c.user)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			body := rec.Body.String()
			if exact, ok := strings.CutPrefix(c.want, "="); ok && body != exact ||
				!ok && !strings.Contains(body, c.want) {
				t.Fatalf("body %q, want %q", body, c.want)
			}
			if c.status >= 400 && rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("error Content-Type %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestAccList(t *testing.T) {
	f := newFixture(t)
	at := created.Format(time.RFC3339)
	list := base + local + "/" + lib + "/@v/list"
	f.run(t, []check{
		// Pseudo-versions are listed too, each version with its stored
		// .info's time or else when it was stored; v1.3.0 has no .mod yet.
		{"versions in order", "", list, "reader", 200, "=" + pseudo + " " + at + "\n" +
			"v1.0.0 " + at + "\nv1.1.0 2020-01-02T03:04:05Z\nv1.2.0-rc.1 " + at + "\n"},
		{
			"pseudo-versions only",
			"",
			base + local + "/example.com/acme/pseudo/@v/list",
			"reader",
			200,
			"=" + pseudo + " " + at + "\n",
		},
		{
			"unknown module",
			"",
			base + local + "/example.com/acme/none/@v/list",
			"reader",
			404,
			"Module example.com/acme/none has no version in go-local.",
		},
		// Only versions whose .mod the caller may read are listed, so a
		// caller who may read none finds none.
		{
			"nothing readable",
			"",
			list,
			"zips",
			404,
			"Module " + lib + " has no version in go-local.",
		},
		{"nobody", "", list, "nobody", 404, "has no version"},
		{
			"federated",
			"",
			base + "go-fed/" + lib + "/@v/list",
			"reader",
			200,
			"=v1.0.0 " + at + "\n",
		},
		{
			"case-encoded",
			"",
			base + local + "/example.com/!acme/upper/@v/list",
			"reader",
			200,
			"=v1.0.0 " + at + "\n",
		},
		{
			"badly case-encoded",
			"",
			base + local + "/example.com/!!acme/upper/@v/list",
			"reader",
			404,
			"is not validly case-encoded.",
		},
		{
			"invalid module path",
			"",
			base + local + "/nodot/lib/@v/list",
			"reader",
			404,
			"Module path nodot/lib is invalid",
		},
		{"head", http.MethodHead, list, "reader", 200, ""},
	})
	if got := f.do(http.MethodGet, list, "reader").
		Header().
		Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("list Content-Type %q", got)
	}
}

func TestAccLatest(t *testing.T) {
	f := newFixture(t)
	f.run(t, []check{
		// The highest release, whose .info is stored.
		{
			"release",
			"",
			base + local + "/" + lib + "/@latest",
			"reader",
			200,
			"=served " + lib + "/@v/v1.1.0.info",
		},
		{
			"latest.info",
			"",
			base + local + "/" + lib + "/@v/latest.info",
			"reader",
			200,
			"=served " + lib + "/@v/v1.1.0.info",
		},
		// Without a release, the highest pre-release, synthesised.
		{
			"pre-release",
			"",
			base + local + "/example.com/acme/pre/@latest",
			"reader",
			200,
			"=" + `{"Version":"v1.0.0-rc.1","Time":"2026-10-16T12:30:45Z"}` + "\n",
		},
		// Without either, the pseudo-version.
		{
			"pseudo-version",
			"",
			base + local + "/example.com/acme/pseudo/@latest",
			"reader",
			200,
			"=" + `{"Version":"` + pseudo + `","Time":"2026-10-16T12:30:45Z"}` + "\n",
		},
		{
			"unknown module",
			"",
			base + local + "/example.com/acme/none/@latest",
			"reader",
			404,
			"has no version",
		},
		{
			"nothing readable",
			"",
			base + local + "/" + lib + "/@latest",
			"zips",
			404,
			"has no version",
		},
		{"head", http.MethodHead, base + local + "/" + lib + "/@latest", "reader", 200, ""},
	})
}

func TestAccInfo(t *testing.T) {
	f := newFixture(t)
	v := base + local + "/" + lib + "/@v/"
	f.run(t, []check{
		{
			"synthesised",
			"",
			v + "v1.0.0.info",
			"reader",
			200,
			"=" + `{"Version":"v1.0.0","Time":"2026-10-16T12:30:45Z"}` + "\n",
		},
		{"stored", "", v + "v1.1.0.info", "reader", 200, "=served " + lib + "/@v/v1.1.0.info"},
		// A stored .info needs Read on it, a synthesised one Read on the
		// .mod it stands for.
		{"stored, readable .info only", "", v + "v1.1.0.info", "infos", 200, "served"},
		{"synthesised, readable .info only", "", v + "v1.0.0.info", "infos", 403, ""},
		{"no read", "", v + "v1.0.0.info", "zips", 403, ""},
		{"anonymous", "", v + "v1.0.0.info", auth.AnonymousUser, 401, ""},
		{
			"invisible",
			"",
			v + "v1.3.0.info",
			"reader",
			404,
			"Version v1.3.0 of module " + lib + " is not in go-local.",
		},
		{"unknown version", "", v + "v1.9.9.info", "reader", 404, "Version v1.9.9 of module"},
		{
			"unknown module",
			"",
			base + local + "/example.com/acme/none/@v/v1.0.0.info",
			"reader",
			404,
			"is not in go-local.",
		},
		// A branch name is no canonical version.
		{
			"branch",
			"",
			v + "main.info",
			"reader",
			404,
			"Version main of module " + lib + " is invalid",
		},
		{"head", http.MethodHead, v + "v1.0.0.info", "reader", 200, ""},
	})
	rec := f.do(http.MethodGet, v+"v1.0.0.info", "reader")
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("synthesised Content-Type %q", got)
	}
}

func TestAccFiles(t *testing.T) {
	f := newFixture(t)
	v := base + local + "/" + lib + "/@v/"
	f.run(t, []check{
		{"zip", "", v + "v1.0.0.zip", "reader", 200, "=served " + lib + "/@v/v1.0.0.zip"},
		{"mod", "", v + "v1.0.0.mod", "reader", 200, "=served " + lib + "/@v/v1.0.0.mod"},
		{"pseudo-version", "", v + pseudo + ".zip", "reader", 200, "served"},
		{
			"case-encoded",
			"",
			base + local + "/example.com/!acme/upper/@v/v1.0.0.zip",
			"reader",
			200,
			"=served example.com/Acme/upper/@v/v1.0.0.zip",
		},
		{
			"plain spelling",
			"",
			base + local + "/example.com/Acme/upper/@v/v1.0.0.zip",
			"reader",
			200,
			"=served example.com/Acme/upper/@v/v1.0.0.zip",
		},
		{"read on the zip alone", "", v + "v1.0.0.zip", "zips", 200, "served"},
		{"refused", "", v + "v1.0.0.mod", "zips", 403, ""},
		// The refusal comes before the lookup, so it says nothing about
		// whether the version exists.
		{"refused, missing", "", v + "v1.9.9.mod", "zips", 403, ""},
		{
			"invisible",
			"",
			v + "v1.3.0.zip",
			"reader",
			404,
			"Version v1.3.0 of module " + lib + " is not in go-local.",
		},
		{"missing", "", v + "v1.9.9.zip", "reader", 404, "is not in go-local."},
		{"not canonical", "", v + "v1.0.zip", "reader", 404, "is not canonical: use v1.0.0."},
		{
			"not a version file",
			"",
			v + "v1.0.0.tar",
			"reader",
			404,
			"is not a Go module .zip, .mod or .info.",
		},
		{"head", http.MethodHead, v + "v1.0.0.zip", "reader", 200, ""},
	})
}

func TestAccRepositoriesAndPaths(t *testing.T) {
	f := newFixture(t)
	f.run(t, []check{
		{
			"remote",
			"",
			base + "go-remote/" + lib + "/@v/list",
			"reader",
			501,
			"is a remote repository",
		},
		{
			"virtual",
			"",
			base + "go-virtual/" + lib + "/@v/list",
			"reader",
			501,
			"is a virtual repository",
		},
		{
			"other type",
			"",
			base + "gen-local/" + lib + "/@v/list",
			"reader",
			404,
			"is not a go repository",
		},
		{
			"missing repository",
			"",
			base + "nope/" + lib + "/@v/list",
			"reader",
			404,
			"Repository 'nope' does not exist",
		},
		{
			"no operation",
			"",
			base + local + "/" + lib,
			"reader",
			404,
			"names no Go module operation.",
		},
		{
			"no file",
			"",
			base + local + "/" + lib + "/@v/",
			"reader",
			404,
			"names no Go module operation.",
		},
		{"bare repository", "", base + local + "/", "reader", 404, "names no Go module operation."},
		{
			"bad matrix parameter",
			"",
			base + local + "/" + lib + "/@v/v1.0.0.zip;=x",
			"reader",
			400,
			"invalid property key",
		},
		// Any other method on a Go path is 405, and on any other path 404.
		{
			"post on a list",
			http.MethodPost,
			base + local + "/" + lib + "/@v/list",
			"reader",
			405,
			"Method POST is not allowed on",
		},
		{
			"delete on a zip",
			http.MethodDelete,
			base + local + "/" + lib + "/@v/v1.0.0.zip",
			"reader",
			405,
			"is not allowed",
		},
		{
			"post elsewhere",
			http.MethodPost,
			base + local + "/" + lib,
			"reader",
			404,
			"names no Go module operation.",
		},
		// The checksum database answers 404 before anything else, to
		// anyone, in any repository.
		{
			"sumdb",
			"",
			base + local + "/sumdb/sum.golang.org/supported",
			"",
			404,
			"artifactweave does not serve",
		},
		{
			"sumdb, missing repository",
			"",
			base + "nope/sumdb/sum.golang.org/lookup/x",
			"",
			404,
			"does not serve",
		},
		{"sumdb root", "", base + local + "/sumdb", "", 404, "does not serve"},
	})
	if len(f.faults) != 0 {
		t.Fatalf("faults: %v", f.faults)
	}
}

func TestAccIsAbsent(t *testing.T) {
	p := golang.Protocol()
	for path, want := range map[string]bool{
		base + local + "/sumdb/sum.golang.org/supported": true,
		base + local + "/sumdb":                          true,
		base + local + "/sumdbx/a":                       false,
		base + local + "/" + lib + "/@v/list":            false,
		base + local:                                     false,
		"/artifactory/api/npm/npm-local/sumdb/x":         false,
	} {
		if got := p.IsAbsent(path); got != want {
			t.Errorf("IsAbsent(%s) = %v, want %v", path, got, want)
		}
	}
}

// TestAccStoreFaults: a store that fails answers a bare 500 and reports the
// cause, wherever the reads meet it.
func TestAccStoreFaults(t *testing.T) {
	v := base + local + "/" + lib + "/@v/"
	for _, c := range []struct{ fail, path string }{
		{"Find", v + "list"},
		{"Find", v + "v1.0.0.zip"},
		{"Versions", v + "list"},
		{"Children", v + "list"},
		{"Version", v + "v1.0.0.info"},
		{"Get", v + "v1.0.0.zip"},
	} {
		t.Run(c.fail+" "+c.path, func(t *testing.T) {
			seed := inmem.New()
			fail := map[string]bool{}
			f := newFixtureOn(t, seed, faulty{Store: seed, fail: fail})
			fail[c.fail] = true
			rec := f.do(http.MethodGet, c.path, "reader")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if len(f.faults) != 1 || !errors.Is(f.faults[0], errStore) {
				t.Fatalf("faults %v", f.faults)
			}
		})
	}
}

// TestAccListPages: the files of a module's versions are read a page at a
// time, and a version's .zip and .mod on different pages still make it
// visible.
func TestAccListPages(t *testing.T) {
	seed := inmem.New()
	f := newFixtureOn(t, seed, faulty{Store: seed, fail: map[string]bool{"pages of 2": true}})
	rec := f.do(http.MethodGet, base+local+"/"+lib+"/@v/list", "reader")
	at := created.Format(time.RFC3339)
	want := pseudo + " " + at + "\nv1.0.0 " + at + "\nv1.1.0 2020-01-02T03:04:05Z\nv1.2.0-rc.1 " + at + "\n"
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}

var errStore = errors.New("store boom")

// faulty fails the store methods fail names and delegates the rest.
type faulty struct {
	*inmem.Store
	fail map[string]bool
}

func (f faulty) Items() store.ItemStore { return faultyItems{f.Store.Items(), f.fail} }

func (f faulty) Packages() store.PackageStore {
	return faultyPackages{f.Store.Packages(), f.fail}
}

type faultyItems struct {
	store.ItemStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (f faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if f.fail["Get"] {
		return types.Item{}, errStore
	}
	return f.ItemStore.Get(ctx, key, cached, p)
}

//nolint:wrapcheck // test double
func (f faultyItems) Children(
	ctx context.Context,
	key string,
	cached bool,
	folder string,
	p store.Page,
) ([]types.Item, string, error) {
	if f.fail["Children"] {
		return nil, "", errStore
	}
	if f.fail["pages of 2"] {
		p.Limit = 2
	}
	return f.ItemStore.Children(ctx, key, cached, folder, p)
}

type faultyPackages struct {
	store.PackageStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (f faultyPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if f.fail["Find"] {
		return types.Package{}, errStore
	}
	return f.PackageStore.Find(ctx, key, name)
}

//nolint:wrapcheck // test double
func (f faultyPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if f.fail["Versions"] {
		return nil, errStore
	}
	return f.PackageStore.Versions(ctx, id)
}

//nolint:wrapcheck // test double
func (f faultyPackages) Version(
	ctx context.Context,
	id int64,
	v string,
) (types.PackageVersion, error) {
	if f.fail["Version"] {
		return types.PackageVersion{}, errStore
	}
	return f.PackageStore.Version(ctx, id, v)
}
