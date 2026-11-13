package chefhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// pageStore counts the package index reads that return whole version
// records, and can delete versions as one is read.
type pageStore struct {
	pkgroute.Store
	versions, version int
	// drop maps a version, as name@version, to the versions of the
	// cookbook that are deleted just before it is read, as if they were
	// deleted between a catalog's count and its page.
	drop map[string][]string
	// names maps a package ID to its cookbook's name.
	names map[int64]string
	// lost is a cookbook Find reports missing, as if it were deleted
	// after the catalog listed its name.
	lost string
}

func (s *pageStore) Packages() port.PackageStore { return pagePackages{s.Store.Packages(), s} }

type pagePackages struct {
	port.PackageStore
	s *pageStore
}

func (p pagePackages) Find(ctx context.Context, key, name string) (artmodel.Package, error) {
	if name == p.s.lost {
		return artmodel.Package{}, kernel.ErrNotFound
	}
	return p.PackageStore.Find(ctx, key, name)
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
	key := p.s.names[id] + "@" + version
	for _, v := range p.s.drop[key] {
		pv, err := p.PackageStore.Version(ctx, id, v)
		if err == nil {
			err = p.PackageStore.DeleteVersion(ctx, pv.ID)
		}
		if err != nil {
			return artmodel.PackageVersion{}, err
		}
	}
	delete(p.s.drop, key)
	return p.PackageStore.Version(ctx, id, version)
}

// pagedCatalog deploys cookbooks cb00 to cb11 to a local repository whose
// index pageStore counts, each at 1.0.0, 1.2 and 1.10.0, whose maintainer
// and description name the cookbook and the version.
func pagedCatalog(t *testing.T) (*fixture, *pageStore) {
	t.Helper()
	s := &pageStore{drop: map[string][]string{}, names: map[int64]string{}}
	f := newFixtureStore(t, repo.Local, nil,
		func(st pkgroute.Store) pkgroute.Store { s.Store = st; return s })
	for i := range 12 {
		name := fmt.Sprintf("cb%02d", i)
		for _, v := range []string{"1.10.0", "1.0.0", "1.2"} {
			f.deploy(f.c, name, v, describedArchive(t, name, v, "maint "+name+" "+v, "desc "+v))
		}
		p, err := f.db.Packages().Find(t.Context(), f.c.Key, name)
		if err != nil {
			t.Fatal(err)
		}
		s.names[p.ID] = name
	}
	return f, s
}

// catalogJSON is the catalog's answer at start, listing each of the
// cookbooks, as name@version, with the maintainer and description of that
// version, of total.
func catalogJSON(t *testing.T, start, total int, cookbooks ...[2]string) string {
	t.Helper()
	items := make([]map[string]string, 0, len(cookbooks))
	for _, c := range cookbooks {
		items = append(items, map[string]string{
			"cookbook":             "http://example.com" + base + "/api/v1/cookbooks/" + c[0],
			"cookbook_name":        c[0],
			"cookbook_maintainer":  "maint " + c[0] + " " + c[1],
			"cookbook_description": "desc " + c[1],
		})
	}
	body, err := json.Marshal(map[string]any{"total": total, "start": start, "items": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(body) + "\n"
}

func wantCatalog(t *testing.T, f *fixture, path, user, want string) {
	t.Helper()
	got := f.do(http.MethodGet, base+path, user)
	if got.Code != http.StatusOK || got.Body.String() != want {
		t.Fatalf("%s: got %d %s\nwant 200 %s", path, got.Code, got.Body.String(), want)
	}
}

// A local repository's catalog reads the record of only the highest version
// of each cookbook on its page (#1404): every cookbook is counted from its
// versions' names, so the total is the whole catalog's, and the page lists
// the description and maintainer of each of its cookbooks' highest
// version the caller may read, as it did when it read every version.
func TestUnitHostedCatalogReadsOnlyItsPage(t *testing.T) {
	f, s := pagedCatalog(t)
	for _, tc := range []struct {
		path  string
		start int
		want  []string
	}{
		{"/api/v1/cookbooks?start=9&items=2", 9, []string{"cb09", "cb10"}},
		{"/api/v1/cookbooks?start=10", 10, []string{"cb10", "cb11"}},
		{"/api/v1/cookbooks?start=12&items=5", 12, nil},
	} {
		s.versions, s.version = 0, 0
		var cookbooks [][2]string
		for _, name := range tc.want {
			cookbooks = append(cookbooks, [2]string{name, "1.10.0"})
		}
		wantCatalog(t, f, tc.path, "admin", catalogJSON(t, tc.start, 12, cookbooks...))
		if s.versions != 0 || s.version != len(tc.want) {
			t.Fatalf("%s read %d version lists and %d versions", tc.path, s.versions, s.version)
		}
	}
	// Read permission decides which cookbooks are counted and which
	// version is the highest.
	grant(t, f, map[string]accesstest.Target{f.c.Key: {
		Include: []string{"**"},
		Exclude: []string{"cb03/**", "**/*-1.10.0.tar.gz"},
	}})
	wantCatalog(t, f, "/api/v1/cookbooks?start=2&items=2", "reader",
		catalogJSON(t, 2, 11, [2]string{"cb02", "1.2"}, [2]string{"cb04", "1.2"}))
}

// A page checks the release it lists as the whole catalog was checked: a
// page cookbook whose highest version's index entry does not match fails.
// A cookbook whose highest version was deleted after the count lists the
// next one, and one with none left leaves the page short. A version that
// fails, but that the page does not list, no longer fails it.
func TestUnitHostedCatalogPageChecks(t *testing.T) {
	f, s := pagedCatalog(t)
	corrupt := func(name, version string) {
		t.Helper()
		p, err := f.db.Packages().Find(t.Context(), f.c.Key, name)
		if err != nil {
			t.Fatal(err)
		}
		v, err := f.db.Packages().Version(t.Context(), p.ID, version)
		if err != nil {
			t.Fatal(err)
		}
		v.Metadata = []byte(`{}`)
		if _, err = f.db.Packages().PutVersion(t.Context(), v); err != nil {
			t.Fatal(err)
		}
	}
	corrupt("cb00", "1.10.0")
	corrupt("cb01", "1.0.0")
	got := f.do(http.MethodGet, base+"/api/v1/cookbooks?items=1", "admin")
	if got.Code != http.StatusBadGateway {
		t.Fatalf("corrupt page cookbook: %d %s", got.Code, got.Body.String())
	}
	wantCatalog(t, f, "/api/v1/cookbooks?start=1&items=2", "admin",
		catalogJSON(t, 1, 12, [2]string{"cb01", "1.10.0"}, [2]string{"cb02", "1.10.0"}))
	s.drop["cb02@1.10.0"] = []string{"1.10.0"}
	s.drop["cb03@1.10.0"] = []string{"1.10.0", "1.0.0", "1.2"}
	s.versions = 0
	wantCatalog(t, f, "/api/v1/cookbooks?start=2&items=3", "admin",
		catalogJSON(t, 2, 12, [2]string{"cb02", "1.2"}, [2]string{"cb04", "1.10.0"}))
	if s.versions != 2 {
		t.Fatalf("the cookbooks whose highest version went read %d version lists", s.versions)
	}
	// A cookbook with no version left, or deleted after its name was
	// listed, is no longer counted.
	s.lost = "cb07"
	wantCatalog(t, f, "/api/v1/cookbooks?start=1&items=1", "admin",
		catalogJSON(t, 1, 10, [2]string{"cb01", "1.10.0"}))
}
