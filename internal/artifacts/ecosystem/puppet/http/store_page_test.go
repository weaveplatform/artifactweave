package puppethttp_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// pageStore counts the package index reads that return whole version
// records, and those of names: every version name of a module, a count or
// a page of them, a tally of the repository's, and a page of its releases
// or modules. It can report a version deleted or an archive missing.
type pageStore struct {
	pkgroute.Store
	versions, version       int
	names, counts, namePage int
	tallies, releasePages   int
	moduleCounts, modules   int
	// gone is a version Version reports deleted; missing is a path whose
	// item Items().Get reports missing.
	gone, missing string
}

func (s *pageStore) Packages() port.PackageStore { return pagePackages{s.Store.Packages(), s} }
func (s *pageStore) Items() port.ItemStore       { return pageItems{s.Store.Items(), s} }

type pagePackages struct {
	port.PackageStore
	s *pageStore
}

func (p pagePackages) Versions(ctx context.Context, id int64) ([]artmodel.PackageVersion, error) {
	p.s.versions++
	return p.PackageStore.Versions(ctx, id)
}

func (p pagePackages) VersionNames(ctx context.Context, id int64) ([]string, error) {
	p.s.names++
	return p.PackageStore.VersionNames(ctx, id)
}

func (p pagePackages) CountVersions(ctx context.Context, id int64) (int, error) {
	p.s.counts++
	return p.PackageStore.CountVersions(ctx, id)
}

func (p pagePackages) VersionNamesAt(
	ctx context.Context,
	id int64,
	newestFirst bool,
	offset, limit int,
) ([]string, error) {
	p.s.namePage++
	return p.PackageStore.VersionNamesAt(ctx, id, newestFirst, offset, limit)
}

func (p pagePackages) TallyVersions(
	ctx context.Context,
	key string,
	limit int,
) (port.VersionTally, error) {
	p.s.tallies++
	return p.PackageStore.TallyVersions(ctx, key, limit)
}

func (p pagePackages) ReleasesAt(
	ctx context.Context,
	key string,
	offset, limit int,
) ([]port.ReleaseName, error) {
	p.s.releasePages++
	return p.PackageStore.ReleasesAt(ctx, key, offset, limit)
}

func (p pagePackages) CountPackages(ctx context.Context, r port.PackageRange) (int, error) {
	p.s.moduleCounts++
	return p.PackageStore.CountPackages(ctx, r)
}

func (p pagePackages) PackagesAt(
	ctx context.Context,
	r port.PackageRange,
	offset, limit int,
) ([]artmodel.Package, error) {
	p.s.modules++
	return p.PackageStore.PackagesAt(ctx, r, offset, limit)
}

func (p pagePackages) Version(
	ctx context.Context,
	id int64,
	version string,
) (artmodel.PackageVersion, error) {
	p.s.version++
	if version == p.s.gone {
		return artmodel.PackageVersion{}, kernel.ErrNotFound
	}
	return p.PackageStore.Version(ctx, id, version)
}

type pageItems struct {
	port.ItemStore
	s *pageStore
}

func (i pageItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (artmodel.Item, error) {
	if path == i.s.missing {
		return artmodel.Item{}, kernel.ErrNotFound
	}
	return i.ItemStore.Get(ctx, key, cached, path)
}

// pagedModule deploys acme-widget at each of versions to a local
// repository whose index pageStore counts, and returns the fixture, the
// store and the releases by version.
func pagedModule(t *testing.T, versions ...string) (*fixture, *pageStore, map[string]listRelease) {
	t.Helper()
	var s *pageStore
	f := newFixtureStore(t, repo.Local, nil,
		func(st pkgroute.Store) pkgroute.Store { s = &pageStore{Store: st}; return s })
	rels := map[string]listRelease{}
	for _, v := range versions {
		rels[v] = f.deployRelease(f.c, "acme-widget", v, moduleArchive(t, "acme-widget", v))
	}
	return f, s, rels
}

// newestFirst is the order the release list answers widgetVersions in, as
// semantic versioning orders them: 1.0.0-1 before 1.0.0, where Maven, the
// package index's order before #1426, put it after.
var newestFirst = []string{
	"2.0.0-rc.1", "1.10.0", "1.9.0", "1.8.0", "1.7.0", "1.6.0", "1.5.0", "1.4.0", "1.3.0",
	"1.2.0", "1.1.0", "1.0.0", "1.0.0-1",
}

// pageJSON is the one-module list's answer for the page of rels at offset
// of limit, of total releases, whose results are versions.
func pageJSON(rels map[string]listRelease, limit, offset, total int, versions ...string) string {
	results := make([]string, 0, len(versions))
	for _, v := range versions {
		results = append(results, releaseJSON(rels[v]))
	}
	return listJSON("/v3/releases?module=acme-widget", limit, offset, total, results...)
}

// A local repository's release list reads the records of its page alone
// (#1404): every release is counted and ordered by its version, so the
// total and the links are the whole list's, and only the page's releases
// are read and checked against their archives. The answer is the one it
// gave when it read every release. A caller who may read the module whole
// has the store count the names and read the page's (#1426); anyone else
// has every name read.
func TestUnitReleaseListReadsOnlyItsPage(t *testing.T) {
	f, s, rels := pagedModule(t, newestFirst...)
	for _, tc := range []struct {
		limit, offset int
		want          []string
	}{
		{3, 9, newestFirst[9:12]},
		{3, 12, newestFirst[12:]},
		{20, 0, newestFirst},
		{5, 13, nil},
	} {
		*s = pageStore{Store: s.Store}
		wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget&limit="+
			strconv.Itoa(tc.limit)+"&offset="+strconv.Itoa(tc.offset), "admin"),
			pageJSON(rels, tc.limit, tc.offset, len(newestFirst), tc.want...))
		if want := (pageStore{Store: s.Store, version: len(tc.want), counts: 1, namePage: 1}); *s != want {
			t.Fatalf("page %d at %d read %+v", tc.limit, tc.offset, *s)
		}
	}
	// Read permission counts and pages only the releases the caller may
	// read.
	f.grant(map[string]accesstest.Target{f.c.Key: {
		Include: []string{"**"}, Exclude: []string{"**/acme-widget-1.9.0.tar.gz"},
	}})
	*s = pageStore{Store: s.Store}
	wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget&limit=2&offset=1", "viewer"),
		pageJSON(rels, 2, 1, len(newestFirst)-1, "1.10.0", "1.8.0"))
	if want := (pageStore{Store: s.Store, version: 2, names: 1}); *s != want {
		t.Fatalf("viewer's page read %+v", *s)
	}
}

// A page checks its releases as the whole list was checked: a page holding
// two that fail answers the failure of the one the index lists first, and
// a release deleted between the count and the page leaves the page short.
// A release that fails outside the page no longer fails it, as the
// whole-repository lists' pages already answered.
func TestUnitReleaseListPageChecks(t *testing.T) {
	f, s, rels := pagedModule(t, "1.0.0", "1.1.0", "1.2.0")
	pkg, err := f.db.Packages().Find(t.Context(), f.c.Key, "acme-widget")
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
	s.missing = "acme-widget/acme-widget-1.2.0.tar.gz"
	list := base + "/v3/releases?module=acme-widget"
	wantError(t, f.do("GET", list, "admin"), 502, "invalid stored Puppet package index")
	wantError(t, f.do("GET", list+"&limit=1", "admin"), 502, "invalid stored Puppet archive")
	wantBody(t, f.do("GET", list+"&limit=1&offset=1", "admin"),
		pageJSON(rels, 1, 1, 3, "1.1.0"))
	s.missing, s.gone = "", "1.2.0"
	wantBody(t, f.do("GET", list+"&limit=2", "admin"), pageJSON(rels, 2, 0, 3, "1.1.0"))
}

// The whole-repository lists count and order every release by its version
// alone and read only their page's: the release list the page's releases,
// and the module list the releases of its page's modules. For a caller who
// may read the whole repository the store tallies the releases and pages
// them (#1426); a caller whose permission names paths has every name read,
// and gets the same answer.
func TestUnitWholeListsReadOnlyTheirPage(t *testing.T) {
	f, s, rels := pagedModule(t, newestFirst...)
	beta := f.deployRelease(f.c, "acme-beta", "2.0.0", moduleArchive(t, "acme-beta", "2.0.0"))
	f.grantRead("lister", "lister", f.c.Key, "acme-*/**")
	for _, c := range []struct {
		user              string
		releases, modules pageStore
	}{
		{
			"admin",
			pageStore{version: 2, tallies: 1, releasePages: 1},
			pageStore{versions: 1, tallies: 1, moduleCounts: 1, modules: 1},
		},
		{"lister", pageStore{version: 2, names: 2}, pageStore{versions: 1, names: 2}},
	} {
		*s = pageStore{Store: s.Store}
		wantBody(t, f.do("GET", base+"/v3/releases?limit=2&offset=10", c.user),
			listJSON("/v3/releases", 2, 10, 14, releaseJSON(rels["1.2.0"]), releaseJSON(rels["1.1.0"])))
		if c.releases.Store = s.Store; *s != c.releases {
			t.Fatalf("%s's release list read %+v", c.user, *s)
		}
		*s = pageStore{Store: s.Store}
		wantBody(t, f.do("GET", base+"/v3/modules?limit=1", c.user),
			listJSON("/v3/modules", 1, 0, 2, moduleJSON(beta)))
		if c.modules.Store = s.Store; *s != c.modules {
			t.Fatalf("%s's module list read %+v", c.user, *s)
		}
	}
}
