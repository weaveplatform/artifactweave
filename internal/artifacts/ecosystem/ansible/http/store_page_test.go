package ansiblehttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/ansible"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// pageStore counts the package index reads that return whole version
// records, and those of version names: every name, a count of them and a
// page of them. It can report a version deleted or a collection missing.
type pageStore struct {
	pkgroute.Store
	versions, version       int
	names, counts, namePage int
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

func (p pagePackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
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

func (p pagePackages) Version(
	ctx context.Context,
	id int64,
	version string,
) (model.PackageVersion, error) {
	p.s.version++
	if version == p.s.gone {
		return model.PackageVersion{}, kernel.ErrNotFound
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
) (model.Item, error) {
	if path == i.s.missing {
		return model.Item{}, kernel.ErrNotFound
	}
	return i.ItemStore.Get(ctx, key, cached, path)
}

// pagedCollection publishes weave.sample at each of versions to a local
// repository whose index pageStore counts.
func pagedCollection(t *testing.T, versions ...string) (*fixture, *pageStore) {
	t.Helper()
	f := newFixture(t)
	for _, v := range versions {
		if _, err := f.items.Deploy(t.Context(), item.Deploy{
			Target: item.Target{Repo: f.repo, Path: ansiblefmt.Path("weave", "sample", v)},
			Body:   bytes.NewReader(versionArchive(t, f.data, v)), By: "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := &pageStore{Store: f.db}
	f.store = s
	f.mount(nil)
	return f, s
}

// versionsJSON is the version list's answer: the versions of its page,
// the link to the next page at next of limit, when there is one, and
// count, the versions of the whole list.
func versionsJSON(t *testing.T, count, limit, next int, versions ...string) string {
	t.Helper()
	data := make([]map[string]string, 0, len(versions))
	for _, v := range versions {
		data = append(data, map[string]string{"version": v})
	}
	var link any
	if next < count {
		link = "/artifactory/api/ansible/ansible-local/api/v3/collections/weave/sample/versions/" +
			"?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(next)
	}
	body, err := json.Marshal(map[string]any{
		"meta": map[string]int{
			"count": count,
		},
		"links": map[string]any{"next": link, "previous": nil},
		"data":  data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body) + "\n"
}

func wantVersions(t *testing.T, got string, code int, want string) {
	t.Helper()
	if code != http.StatusOK || got != want {
		t.Fatalf("got %d %s\nwant 200 %s", code, got, want)
	}
}

// newestFirst is the order the version list answers its versions in, as
// semantic versioning orders them: 1.0.0-1 before 1.0.0, where Maven, the
// package index's order before #1426, put it after.
var newestFirst = []string{
	"2.0.0-rc.1", "1.10.0", "1.9.0", "1.8.0", "1.7.0", "1.6.0", "1.5.0", "1.4.0", "1.3.0",
	"1.2.0", "1.1.0", "1.0.0", "1.0.0-1",
}

const versionsRoute = prefix + "/api/v3/collections/weave/sample/versions/"

// A local repository's version list reads the records of its page alone
// (#1404): every version is counted and ordered by its name, so the count
// and the next link are the whole list's, and only the page's versions are
// read and checked against their stored collections. The answer is the
// one it gave when it read every version.
func TestUnitGalaxyVersionListReadsOnlyItsPage(t *testing.T) {
	f, s := pagedCollection(t, newestFirst...)
	for _, tc := range []struct {
		limit, offset int
		want          []string
	}{
		{3, 9, newestFirst[9:12]},
		{3, 12, newestFirst[12:]},
		{100, 0, newestFirst},
		{5, 13, nil},
		{5, 100000, nil},
	} {
		*s = pageStore{Store: s.Store}
		w := f.do(http.MethodGet, versionsRoute+"?limit="+strconv.Itoa(tc.limit)+"&offset="+
			strconv.Itoa(tc.offset), nil, "", "")
		wantVersions(t, w.Body.String(), w.Code, versionsJSON(t, len(newestFirst), tc.limit,
			min(tc.offset, len(newestFirst))+tc.limit, tc.want...))
		// The administrator may read the whole collection: one count and
		// one page of names (#1426).
		if s.versions != 0 || s.version != len(tc.want) || s.names != 0 ||
			s.counts != 1 || s.namePage != 1 {
			t.Fatalf("page %d at %d read %+v", tc.limit, tc.offset, *s)
		}
	}
	// Read permission counts and pages only the versions the caller may
	// read.
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "all-but-one",
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{f.repo.Key: {
					Include: []string{"**"}, Exclude: []string{"collections/weave/sample/1.9.0/**"},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	*s = pageStore{Store: s.Store}
	w := f.do(http.MethodGet, versionsRoute+"?limit=2&offset=1", nil, "", "reader")
	wantVersions(t, w.Body.String(), w.Code,
		versionsJSON(t, len(newestFirst)-1, 2, 3, "1.10.0", "1.8.0"))
	if s.version != 2 || s.names != 1 || s.counts != 0 || s.namePage != 0 {
		t.Fatalf("reader's page read %+v", *s)
	}
}

// A late page of a collection of 1,000 versions, for a caller who may read
// it whole, reads one count and one page of names, and the page's
// versions (#1426); a caller whose permission names paths reads every
// name, as before. The store's own test pages 5,000
// (TestAccPackagePagesAtScale); each write here reorders the whole
// collection, so this one is kept smaller.
func TestUnitGalaxyVersionListLatePage(t *testing.T) {
	const count, page = 1000, 20
	oldest := make([]string, 0, page)
	for i := page - 1; i >= 0; i-- {
		oldest = append(oldest, "0.0."+strconv.Itoa(i))
	}
	f, s := pagedCollection(t, oldest...)
	pkg, err := f.db.Packages().Find(t.Context(), f.repo.Key, "weave.sample")
	if err != nil {
		t.Fatal(err)
	}
	// The newer go straight into the index, from the lowest up, which
	// keeps each write's reorder of the package short; no page reads their
	// records.
	for i := range count - page {
		if _, err := f.db.Packages().PutVersion(t.Context(), model.PackageVersion{
			PackageID: pkg.ID, Version: "1.0." + strconv.Itoa(i), Metadata: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Every archive's path, but no pattern that names a folder whole.
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "archives",
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{f.repo.Key: {
					Include: []string{"collections/weave/sample/*/*.tar.gz"},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"", "reader"} {
		*s = pageStore{Store: s.Store}
		w := f.do(http.MethodGet, versionsRoute+"?limit=20&offset=980", nil, "", user)
		wantVersions(t, w.Body.String(), w.Code, versionsJSON(t, count, page, count, oldest...))
		want := pageStore{Store: s.Store, version: page, counts: 1, namePage: 1}
		if user != "" {
			want = pageStore{Store: s.Store, version: page, names: 1}
		}
		if *s != want {
			t.Fatalf("%q read %+v", user, *s)
		}
	}
}
