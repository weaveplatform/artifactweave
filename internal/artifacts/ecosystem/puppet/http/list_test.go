package puppethttp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// listRelease is one release the list tests deploy, with its archive.
type listRelease struct {
	name, version string
	data          []byte
}

// moduleArchive returns an archive of module name at version that holds
// only its metadata.json, whose summary names the release.
func moduleArchive(t *testing.T, name, version string) []byte {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"name": name, "version": version, "summary": name + " " + version,
		"dependencies": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name + "-" + version + "/metadata.json", Mode: 0o644,
		Size: int64(len(manifest)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// deployList deploys acme-alpha 1.0.0 and 1.1.0 and acme-beta 2.0.0 to c,
// and returns them in the order the release list answers them.
func (f *fixture) deployList(c repo.Config) []listRelease {
	f.t.Helper()
	var out []listRelease
	for _, rel := range [][2]string{
		{"acme-beta", "2.0.0"}, {"acme-alpha", "1.0.0"}, {"acme-alpha", "1.1.0"},
	} {
		data := moduleArchive(f.t, rel[0], rel[1])
		if _, err := f.items.Deploy(f.t.Context(), item.Deploy{
			Target: item.Target{Repo: c, Path: rel[0] + "/" + rel[0] + "-" + rel[1] + ".tar.gz"},
			Body:   bytes.NewReader(data),
			By:     "admin",
		}); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, listRelease{rel[0], rel[1], data})
	}
	return []listRelease{out[2], out[1], out[0]}
}

// releaseJSON is the record every release list answers for rel.
func releaseJSON(rel listRelease) string {
	slug := rel.name + "-" + rel.version
	return fmt.Sprintf(
		`{"uri":"/v3/releases/%[1]s","slug":"%[1]s","version":"%[2]s",`+
			`"file_uri":"/v3/files/%[1]s.tar.gz","file_sha256":"%[3]s","file_size":%[4]d,`+
			`"metadata":{"name":"%[5]s","version":"%[2]s","summary":"%[5]s %[2]s","dependencies":[]},`+
			`"tags":[]}`,
		slug, rel.version, sum(rel.data), len(rel.data), rel.name,
	)
}

// moduleJSON is the record the module search and the module list answer
// for a module whose releases are rels, newest first.
func moduleJSON(rels ...listRelease) string {
	name := rels[0].name
	owner, module, _ := strings.Cut(name, "-")
	var versions []string
	for _, rel := range rels {
		versions = append(versions, fmt.Sprintf(
			`{"uri":"/v3/releases/%s-%s","version":"%s"}`, name, rel.version, rel.version))
	}
	return fmt.Sprintf(
		`{"current_release":%s,"name":"%s","owner":{"username":"%s"},"releases":[%s],`+
			`"slug":"%s","uri":"/v3/modules/%s"}`,
		releaseJSON(rels[0]), module, owner, strings.Join(versions, ","), name, name,
	)
}

// listJSON is a whole-repository list's answer: its pagination, as 7.x's
// sample answer for Get Puppet Modules gives it, and results.
func listJSON(route string, limit, offset, total int, results ...string) string {
	link := func(offset int) string {
		return fmt.Sprintf(`"%s?limit=%d&offset=%d"`, route, limit, offset)
	}
	previous, next := "null", "null"
	if offset > 0 {
		previous = link(max(0, offset-limit))
	}
	if offset+limit < total {
		next = link(offset + limit)
	}
	return fmt.Sprintf(
		`{"pagination":{"limit":%d,"offset":%d,"first":%s,"previous":%s,"current":%s,`+
			`"next":%s,"total":%d},"results":[%s]}`,
		limit, offset, link(0), previous, link(offset), next, total, strings.Join(results, ","),
	) + "\n"
}

// wantBody fails unless r is a 200 whose body is want.
func wantBody(t *testing.T, r *httptest.ResponseRecorder, want string) {
	t.Helper()
	if r.Code != 200 || r.Body.String() != want {
		t.Fatalf("got %d %s\nwant %s", r.Code, r.Body.String(), want)
	}
}

// wantError fails unless r is status with message as its only error, in
// Artifactory's envelope.
func wantError(t *testing.T, r *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	want := fmt.Sprintf(`{"errors":[{"message":%q,"status":%d}]}`+"\n", message, status)
	if r.Code != status || r.Body.String() != want {
		t.Fatalf("got %d %s\nwant %d %s", r.Code, r.Body.String(), status, want)
	}
}

// grantRead gives user Read on the paths include names in key.
func (f *fixture) grantRead(name, user, key string, include ...string) {
	f.t.Helper()
	if err := f.db.Permissions().Create(f.t.Context(), accesstest.Permission{
		Name: name,
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{user: api.ActionRead}},
				Targets: map[string]accesstest.Target{key: {Include: include}},
			},
		},
	}); err != nil {
		f.t.Fatal(err)
	}
}

// A local or federated repository answers v3/releases and v3/modules
// without a query with every release and module it hosts, paged in the
// Forge's pagination object (7.x's Get Puppet Releases and Get Puppet
// Modules; JFrog's sample answer; RTFACT-14028, RTFACT-14274).
func TestUnitHostedListsAnswerEveryReleaseAndModule(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			rels := f.deployList(f.c)
			alpha, beta := moduleJSON(rels[0], rels[1]), moduleJSON(rels[2])
			wantBody(
				t,
				f.do("GET", base+"/v3/releases", "admin"),
				listJSON("/v3/releases", 20, 0, 3,
					releaseJSON(rels[0]), releaseJSON(rels[1]), releaseJSON(rels[2])),
			)
			wantBody(t, f.do("GET", base+"/v3/modules", "admin"),
				listJSON("/v3/modules", 20, 0, 2, alpha, beta))
			for _, route := range []string{"/v3/releases", "/v3/modules"} {
				r := f.do("HEAD", base+route, "admin")
				if r.Code != 200 || r.Body.Len() != 0 ||
					r.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("HEAD %s: %d %v %s", route, r.Code, r.Header(), r.Body.String())
				}
			}
		})
	}
}

// limit and offset page a list, and each link keeps both; the last page has
// no next link. A limit above 100 is served as 100, as 7.x serves it
// (RTFACT-30529).
func TestUnitHostedListsPage(t *testing.T) {
	f := newFixture(t, repo.Local)
	rels := f.deployList(f.c)
	releases := base + "/v3/releases"
	wantBody(t, f.do("GET", releases+"?limit=1&offset=1", "admin"),
		listJSON("/v3/releases", 1, 1, 3, releaseJSON(rels[1])))
	r := f.do("GET", releases+"?limit=1&offset=1", "admin")
	if !strings.Contains(r.Body.String(), `"next":"/v3/releases?limit=1&offset=2"`) {
		t.Fatalf("next: %s", r.Body.String())
	}
	wantBody(t, f.do("GET", releases+"?offset=2&limit=1", "admin"),
		listJSON("/v3/releases", 1, 2, 3, releaseJSON(rels[2])))
	if r := f.do("GET", releases+"?offset=2&limit=1", "admin"); !strings.Contains(
		r.Body.String(), `"next":null`) {
		t.Fatalf("last page: %s", r.Body.String())
	}
	wantBody(t, f.do("GET", releases+"?limit=2", "admin"),
		listJSON("/v3/releases", 2, 0, 3, releaseJSON(rels[0]), releaseJSON(rels[1])))
	wantBody(t, f.do("GET", releases+"?limit=500", "admin"), listJSON("/v3/releases", 100, 0, 3,
		releaseJSON(rels[0]), releaseJSON(rels[1]), releaseJSON(rels[2])))
	wantBody(t, f.do("GET", releases+"?offset=9", "admin"), listJSON("/v3/releases", 20, 9, 3))
	modules := base + "/v3/modules"
	wantBody(t, f.do("GET", modules+"?limit=1", "admin"),
		listJSON("/v3/modules", 1, 0, 2, moduleJSON(rels[0], rels[1])))
	wantBody(t, f.do("GET", modules+"?limit=1&offset=1", "admin"),
		listJSON("/v3/modules", 1, 1, 2, moduleJSON(rels[2])))
	for _, route := range []string{"/v3/releases", "/v3/modules"} {
		for query, message := range map[string]string{
			"?limit=0":   "invalid Puppet limit",
			"?limit=x":   "invalid Puppet limit",
			"?offset=-1": "invalid Puppet offset",
			"?offset=x":  "invalid Puppet offset",
		} {
			wantError(t, f.do("GET", base+route+query, "admin"), 400, message)
		}
	}
}

// A list holds only what the caller may read, and its total counts only
// that; a caller who may read nothing in the repository is refused, 401
// when anonymous (DV-057).
func TestUnitHostedListsFilterByReadPermission(t *testing.T) {
	f := newFixture(t, repo.Local)
	rels := f.deployList(f.c)
	f.grantRead("alpha-only", "alpha", f.c.Key, "acme-alpha/**")
	f.grantRead("old-alpha", "old", f.c.Key, "acme-alpha/acme-alpha-1.0.0.tar.gz")
	wantBody(t, f.do("GET", base+"/v3/releases", "alpha"),
		listJSON("/v3/releases", 20, 0, 2, releaseJSON(rels[0]), releaseJSON(rels[1])))
	wantBody(t, f.do("GET", base+"/v3/modules", "alpha"),
		listJSON("/v3/modules", 20, 0, 1, moduleJSON(rels[0], rels[1])))
	wantBody(t, f.do("GET", base+"/v3/modules", "old"),
		listJSON("/v3/modules", 20, 0, 1, moduleJSON(rels[1])))
	for _, route := range []string{"/v3/releases", "/v3/modules"} {
		wantError(t, f.do("GET", base+route, "denied"), 403, "Forbidden")
		if r := f.do("GET", base+route, ""); r.Code != 401 ||
			r.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("anonymous %s: %d %v", route, r.Code, r.Header())
		}
	}
}

// A blacked-out repository refuses both lists with the blackout's 404,
// naming the request's own path, after the caller's read permission, as
// it refuses one module's list (R2a, R9, R10); patterns that reject a
// module's archives leave its releases listed (R3).
func TestUnitHostedListsUnderTheDownloadPolicy(t *testing.T) {
	const blackedOut = "The repository 'puppet-local' is blacked out and cannot serve artifact "
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			rels := f.deployList(f.c)
			f.c.ExcludesPattern = new("acme-alpha/**")
			f.save(f.c)
			wantBody(t, f.do("GET", base+"/v3/releases?limit=1", "admin"),
				listJSON("/v3/releases", 1, 0, 3, releaseJSON(rels[0])))
			wantBody(t, f.do("GET", base+"/v3/modules?limit=1", "admin"),
				listJSON("/v3/modules", 1, 0, 2, moduleJSON(rels[0], rels[1])))
			f.c.BlackedOut = new(true)
			f.save(f.c)
			for _, route := range []string{"v3/releases", "v3/modules"} {
				wantPolicy(t, f.do("GET", base+"/"+route+"?limit=1", "admin"),
					blackedOut+"'puppet-local:"+route+"'.")
				if r := f.do("HEAD", base+"/"+route, "admin"); r.Code != 404 {
					t.Fatalf("HEAD %s: %d", route, r.Code)
				}
				wantError(t, f.do("GET", base+"/"+route, "denied"), 403, "Forbidden")
			}
		})
	}
}

// A virtual repository answers both lists 404, as 7.x does (RTFACT-15072),
// after the caller's read permission; a remote repository keeps the 400 it
// gave before, whatever page the query names.
func TestUnitListsOfVirtualAndRemoteRepositories(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.deployList(f.c)
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{f.c.Key},
	})
	f.grantRead("virtual-reader", "viewer", "virtual", "**")
	for _, route := range []string{"/v3/releases", "/v3/modules?limit=x"} {
		path := "/artifactory/api/puppet/virtual" + route
		wantError(t, f.do("GET", path, "admin"), 404, "404 Not found")
		wantError(t, f.do("GET", path, "viewer"), 404, "404 Not found")
		if r := f.do("HEAD", path, "admin"); r.Code != 404 {
			t.Fatalf("HEAD %s: %d", route, r.Code)
		}
		wantError(t, f.do("GET", path, "denied"), 403, "Forbidden")
		if r := f.do("GET", path, ""); r.Code != 401 {
			t.Fatalf("anonymous %s: %d", route, r.Code)
		}
	}
	remote := newFixtureWith(t, repo.Remote,
		func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = metadataSource{} })
	for route, message := range map[string]string{
		"/v3/releases":              "invalid Puppet module",
		"/v3/releases?limit=x":      "invalid Puppet module",
		"/v3/releases?offset=1":     "invalid Puppet module",
		"/v3/modules":               "search requires an exact Puppet module name",
		"/v3/modules?limit=1":       "search requires an exact Puppet module name",
		"/v3/modules?offset=x":      "search requires an exact Puppet module name",
		"/v3/modules?limit=1&x=bad": "search requires an exact Puppet module name",
	} {
		wantError(t, remote.do("GET", base+route, "admin"), 400, message)
	}
}

// The one-module list and the module search answer as they did, and a
// query that names more than a page keeps the 400 it had.
func TestUnitListsLeaveTheOneModuleRoutes(t *testing.T) {
	f := newFixture(t, repo.Local)
	rels := f.deployList(f.c)
	r := f.do("GET", base+"/v3/releases?module=acme-alpha", "admin")
	want := `{"results":[` + releaseJSON(rels[0]) + `,` + releaseJSON(rels[1]) +
		`],"pagination":{"limit":1000,"offset":0,"total":2,"next":null}}` + "\n"
	if r.Code != 200 || r.Body.String() != want {
		t.Fatalf("module list: %d %s\nwant %s", r.Code, r.Body.String(), want)
	}
	r = f.do("GET", base+"/v3/releases?module=acme-alpha&limit=1", "admin")
	if r.Code != 200 || !strings.Contains(r.Body.String(),
		`"next":"/v3/releases?module=acme-alpha`+"\\u0026"+`limit=1`+"\\u0026"+`offset=1"`) {
		t.Fatalf("module list page: %d %s", r.Code, r.Body.String())
	}
	r = f.do("GET", base+"/v3/modules?query=acme-beta", "admin")
	want = `{"pagination":{"limit":1000,"offset":0,"total":1,"next":null},"results":[` +
		moduleJSON(rels[2]) + `]}` + "\n"
	if r.Code != 200 || r.Body.String() != want {
		t.Fatalf("search: %d %s\nwant %s", r.Code, r.Body.String(), want)
	}
	for route, message := range map[string]string{
		"/v3/releases?sort_by=version":       "invalid Puppet module",
		"/v3/releases?exclude_fields=x":      "invalid Puppet module",
		"/v3/releases?limit=1&limit=2":       "unsupported Puppet query",
		"/v3/releases?owner=acme":            "unsupported Puppet query",
		"/v3/modules?owner=acme":             "search requires an exact Puppet module name",
		"/v3/modules?limit=1&limit=2":        "search requires an exact Puppet module name",
		"/v3/modules?query=acme-alpha&limit": "search requires an exact Puppet module name",
		"/v3/modules?limit=%zz":              "search requires an exact Puppet module name",
	} {
		wantError(t, f.do("GET", base+route, "admin"), 400, message)
	}
}

// listStore fails the package index reads a list makes, one at a time.
type listStore struct {
	pkgroute.Store
	list, versions, version, huge, many bool
	gone, items, paged                  bool
	moduleGone                          string
}

func (s *listStore) Packages() port.PackageStore { return listPackages{s.Store.Packages(), s} }
func (s *listStore) Items() port.ItemStore       { return listItems{s.Store.Items(), s} }

type listPackages struct {
	port.PackageStore
	s *listStore
}

func (p listPackages) List(
	ctx context.Context,
	key string,
	page kernel.Page,
) ([]artmodel.Package, string, error) {
	if p.s.list {
		return nil, "", context.Canceled
	}
	if p.s.many {
		return make([]artmodel.Package, 4097), "", nil
	}
	if p.s.paged {
		// The index pages its modules: the first page holds them all and
		// names a second, empty one.
		if page.After == "" {
			out, _, err := p.PackageStore.List(ctx, key, page)
			return out, "more", err
		}
		return nil, "", nil
	}
	if p.s.huge {
		// 17 modules of 4,096 versions each pass the per-module bound and
		// together exceed the 65,536 releases a list reads.
		out := make([]artmodel.Package, 17)
		for i := range out {
			out[i].Name = fmt.Sprintf("acme-m%d", i)
		}
		return out, "", nil
	}
	return p.PackageStore.List(ctx, key, page)
}

func (p listPackages) Versions(ctx context.Context, id int64) ([]artmodel.PackageVersion, error) {
	if p.s.versions {
		return nil, context.Canceled
	}
	if p.s.huge {
		return make([]artmodel.PackageVersion, 4096), nil
	}
	return p.PackageStore.Versions(ctx, id)
}

func (p listPackages) Version(
	ctx context.Context,
	id int64,
	version string,
) (artmodel.PackageVersion, error) {
	if p.s.version {
		return artmodel.PackageVersion{}, context.Canceled
	}
	if p.s.gone && version == "1.1.0" {
		return artmodel.PackageVersion{}, kernel.ErrNotFound
	}
	return p.PackageStore.Version(ctx, id, version)
}

func (p listPackages) Find(ctx context.Context, key, name string) (artmodel.Package, error) {
	if name == p.s.moduleGone {
		return artmodel.Package{}, kernel.ErrNotFound
	}
	return p.PackageStore.Find(ctx, key, name)
}

type listItems struct {
	port.ItemStore
	s *listStore
}

func (i listItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (artmodel.Item, error) {
	if i.s.items {
		return artmodel.Item{}, context.Canceled
	}
	return i.ItemStore.Get(ctx, key, cached, path)
}

// A list fails rather than advertise what the index cannot back: a store
// fault answers 500, an index entry or archive that does not match 502,
// and an index beyond the bounds 503. A release or module deleted between
// the count and the page leaves the page short.
func TestUnitHostedListsStoreFailures(t *testing.T) {
	for _, tc := range []struct {
		name, route string
		tune        func(*listStore)
		status      int
		message     string
	}{
		{"list", "/v3/releases", func(s *listStore) { s.list = true }, 500, "internal error"},
		{"versions", "/v3/modules", func(s *listStore) { s.versions = true }, 500, "internal error"},
		{"version", "/v3/releases", func(s *listStore) { s.version = true }, 500, "internal error"},
		{
			"items", "/v3/releases", func(s *listStore) { s.items = true }, 502,
			"invalid stored Puppet archive",
		},
		{
			"module items", "/v3/modules", func(s *listStore) { s.items = true }, 502,
			"invalid stored Puppet archive",
		},
		{
			"modules", "/v3/modules", func(s *listStore) { s.many = true }, 503,
			"Puppet modules exceed limit",
		},
		{
			"releases", "/v3/releases", func(s *listStore) { s.huge = true }, 503,
			"Puppet releases exceed limit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *listStore
			f := newFixtureStore(t, repo.Local, nil,
				func(st pkgroute.Store) pkgroute.Store { s = &listStore{Store: st}; return s })
			f.deployList(f.c)
			tc.tune(s)
			wantError(t, f.do("GET", base+tc.route, "admin"), tc.status, tc.message)
		})
	}
	var s *listStore
	f := newFixtureStore(t, repo.Local, nil,
		func(st pkgroute.Store) pkgroute.Store { s = &listStore{Store: st}; return s })
	rels := f.deployList(f.c)
	s.gone, s.moduleGone, s.paged = true, "acme-beta", true
	wantBody(t, f.do("GET", base+"/v3/releases", "admin"), listJSON("/v3/releases", 20, 0, 3,
		releaseJSON(rels[1]), releaseJSON(rels[2])))
	wantBody(t, f.do("GET", base+"/v3/modules", "admin"),
		listJSON("/v3/modules", 20, 0, 2, moduleJSON(rels[0], rels[1])))
	s.gone, s.moduleGone = false, ""
	pkg, err := f.db.Packages().Find(t.Context(), f.c.Key, "acme-alpha")
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Packages().Version(t.Context(), pkg.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	v.Metadata = []byte(`{}`)
	if _, err = f.db.Packages().PutVersion(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do("GET", base+"/v3/releases?offset=1&limit=1", "admin"), 502,
		"invalid stored Puppet package index")
	wantBody(t, f.do("GET", base+"/v3/releases?limit=1", "admin"),
		listJSON("/v3/releases", 1, 0, 3, releaseJSON(rels[0])))
}
