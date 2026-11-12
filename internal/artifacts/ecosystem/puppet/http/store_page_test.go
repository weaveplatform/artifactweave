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
// records, and can report a version deleted or an archive missing.
type pageStore struct {
	pkgroute.Store
	versions, version int
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

// newestFirst is the order the release list answers widgetVersions in. The
// package index orders 1.0.0-1 after 1.0.0, as Maven does, and the list
// before it, as semantic versioning does.
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
// gave when it read every release.
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
		s.versions, s.version = 0, 0
		wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget&limit="+
			strconv.Itoa(tc.limit)+"&offset="+strconv.Itoa(tc.offset), "admin"),
			pageJSON(rels, tc.limit, tc.offset, len(newestFirst), tc.want...))
		if s.versions != 0 || s.version != len(tc.want) {
			t.Fatalf("page %d at %d read %d version lists and %d versions", tc.limit, tc.offset,
				s.versions, s.version)
		}
	}
	// Read permission counts and pages only the releases the caller may
	// read.
	f.grant(map[string]accesstest.Target{f.c.Key: {
		Include: []string{"**"}, Exclude: []string{"**/acme-widget-1.9.0.tar.gz"},
	}})
	s.version = 0
	wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget&limit=2&offset=1", "viewer"),
		pageJSON(rels, 2, 1, len(newestFirst)-1, "1.10.0", "1.8.0"))
	if s.version != 2 {
		t.Fatalf("viewer's page read %d versions", s.version)
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
// and the module list the releases of its page's modules.
func TestUnitWholeListsReadOnlyTheirPage(t *testing.T) {
	f, s, rels := pagedModule(t, newestFirst...)
	beta := f.deployRelease(f.c, "acme-beta", "2.0.0", moduleArchive(t, "acme-beta", "2.0.0"))
	s.versions, s.version = 0, 0
	wantBody(t, f.do("GET", base+"/v3/releases?limit=2&offset=10", "admin"),
		listJSON("/v3/releases", 2, 10, 14, releaseJSON(rels["1.2.0"]), releaseJSON(rels["1.1.0"])))
	if s.versions != 0 || s.version != 2 {
		t.Fatalf("release list read %d version lists and %d versions", s.versions, s.version)
	}
	s.versions, s.version = 0, 0
	wantBody(t, f.do("GET", base+"/v3/modules?limit=1", "admin"),
		listJSON("/v3/modules", 1, 0, 2, moduleJSON(beta)))
	if s.versions != 1 || s.version != 0 {
		t.Fatalf("module list read %d version lists and %d versions", s.versions, s.version)
	}
}
