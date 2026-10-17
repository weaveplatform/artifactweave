package maven_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The Maven group's four local types have the Maven format; p2 and the
// other types do not.
func TestServes(t *testing.T) {
	for _, pt := range []repo.PackageType{repo.Maven, repo.Gradle, repo.Ivy, repo.SBT} {
		if !maven.Serves(pt) {
			t.Errorf("%s is not served", pt)
		}
		if _, ok := formats.For(pt).(maven.Handler); !ok {
			t.Errorf("%s: %T", pt, formats.For(pt))
		}
	}
	for _, pt := range []repo.PackageType{repo.P2, repo.Generic, repo.NPM} {
		if maven.Serves(pt) {
			t.Errorf("%s is served", pt)
		}
	}
}

func TestClassify(t *testing.T) {
	h := maven.Handler{}
	unique := repo.Config{SnapshotVersionBehavior: repo.Unique}
	deployer := repo.Config{SnapshotVersionBehavior: repo.Deployer}
	for _, tc := range []struct {
		c    repo.Config
		path string
		want format.Class
	}{
		{unique, "com/acme/app/1.0/app-1.0.jar", format.ClassUnique},
		{unique, "com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar", format.ClassUnique},
		{unique, "com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", format.ClassMetadata},
		{unique, ".index/nexus-maven-repository-index.gz", format.ClassMetadata},
		{unique, "com/acme/app/maven-metadata.xml", format.ClassDerived},
		{unique, "com/acme/app/1.0-SNAPSHOT/maven-metadata.xml", format.ClassDerived},
		{deployer, "com/acme/app/1.0-SNAPSHOT/maven-metadata.xml", format.ClassMetadata},
		{deployer, "com/acme/app/maven-metadata.xml", format.ClassDerived},
	} {
		if got := h.Classify(tc.c, tc.path); got != tc.want {
			t.Errorf("%s under %s: %v", tc.path, tc.c.SnapshotVersionBehavior, got)
		}
	}
}

// world is a Maven repository behind the item service, with a clock that
// moves a minute on every read of it.
type world struct {
	t     *testing.T
	ctx   context.Context
	db    *inmem.Store
	c     repo.Config
	items *item.Service
	now   time.Time
}

func newWorld(t *testing.T, tune func(*repo.Config)) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), db: inmem.New(),
		now: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC),
	}
	c := repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven}
	repo.Defaults(&c)
	if tune != nil {
		tune(&c)
	}
	if err := w.db.Repos().Create(w.ctx, c); err != nil {
		t.Fatal(err)
	}
	w.c = c
	w.items = &item.Service{Store: w.db, Blobs: blobs, Now: func() time.Time {
		w.now = w.now.Add(time.Minute)
		return w.now
	}}
	return w
}

func (w *world) deploy(paths ...string) []types.Item {
	w.t.Helper()
	out := make([]types.Item, 0, len(paths))
	for _, p := range paths {
		it, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: w.c, Path: p}, Body: strings.NewReader(p),
		})
		if err != nil {
			w.t.Fatal(err)
		}
		out = append(out, it)
	}
	return out
}

func (w *world) delete(p string) {
	w.t.Helper()
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "test"); err != nil {
		w.t.Fatal(err)
	}
}

// versions returns the package's versions in index order, each with its
// date and file names.
func (w *world) versions(pkg string) []indexed {
	w.t.Helper()
	p, err := w.db.Packages().Find(w.ctx, w.c.Key, pkg)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	vs, err := w.db.Packages().Versions(w.ctx, p.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]indexed, 0, len(vs))
	for _, v := range vs {
		ids, err := w.db.Packages().Files(w.ctx, v.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		iv := indexed{Version: v.Version, Created: v.Created}
		for _, id := range ids {
			it, err := w.db.Items().GetByID(w.ctx, id)
			if err != nil {
				w.t.Fatal(err)
			}
			iv.Files = append(iv.Files, it.Name)
		}
		slices.Sort(iv.Files)
		out = append(out, iv)
	}
	return out
}

type indexed struct {
	Version string
	Created time.Time
	Files   []string
}

// Versions order by Mercury, a snapshot's unique builds are files of its
// one version, and a version is dated by its earliest file, which a later
// file and a redeploy leave as it is.
func TestIndex(t *testing.T) {
	w := newWorld(t, nil)
	dir := "com/acme/app/"
	w.deploy(
		dir+"1.1/app-1.1.jar",
		dir+"1.0/app-1.0.pom",
		dir+"1.0/app-1.0.jar",
		dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar",
		dir+"1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar",
		dir+"1.0-SNAPSHOT/maven-metadata.xml",
		dir+"1.0-alpha-1/app-1.0-alpha-1.jar",
		dir+"maven-metadata.xml",
		dir+"notes.txt",
		"readme.txt",
	)
	// The redeploy keeps 1.0's date.
	w.deploy(dir + "1.0/app-1.0.pom")
	at := func(minute int) time.Time {
		return time.Date(2026, 10, 17, 9, minute, 0, 0, time.UTC)
	}
	want := []indexed{
		{
			"1.0-SNAPSHOT",
			at(4),
			[]string{"app-1.0-20261017.090000-1.jar", "app-1.0-20261017.100000-2.jar"},
		},
		{"1.0-alpha-1", at(7), []string{"app-1.0-alpha-1.jar"}},
		{"1.0", at(2), []string{"app-1.0.jar", "app-1.0.pom"}},
		{"1.1", at(1), []string{"app-1.1.jar"}},
	}
	if diff := cmp.Diff(want, w.versions("com.acme:app")); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	pkgs, _, err := w.db.Packages().List(w.ctx, w.c.Key, store.Page{Limit: 10})
	if err != nil || len(pkgs) != 1 || pkgs[0].Type != repo.Maven {
		t.Fatalf("packages %+v %v", pkgs, err)
	}

	// Deleting a version's earliest file dates it by the next; deleting its
	// last removes it, and the last version takes the package with it.
	w.delete(dir + "1.0/app-1.0.pom")
	if got := w.versions("com.acme:app")[2]; got.Version != "1.0" || !got.Created.Equal(at(3)) {
		t.Fatalf("1.0 after its POM went: %+v", got)
	}
	w.delete(dir + "1.0-SNAPSHOT")
	if got := len(w.versions("com.acme:app")); got != 3 {
		t.Fatalf("%d versions", got)
	}
	w.delete(dir)
	if got := w.versions("com.acme:app"); got != nil {
		t.Fatalf("the package survived: %+v", got)
	}
}

// layouts resolves bundled layouts, or fails.
type layouts struct{ fail bool }

var errLayout = errors.New("layout boom")

func (l layouts) Layout(_ context.Context, name string) (*layout.Layout, error) {
	if l.fail {
		return nil, errLayout
	}
	return layout.Lookup(name) //nolint:wrapcheck // test double
}

// A repository with no layout reads maven-2-default; one whose layout is
// not defined reads nothing, so it keeps no index; a descriptor that cannot
// be read fails each hook; and a folder or a file no version holds changes
// nothing.
func TestHookLayouts(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	h := maven.Handler{}
	c := repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven}
	it, err := db.Items().Put(ctx, types.Item{
		RepoKey: c.Key, Name: "com/acme/app/1.0/app-1.0.jar", Created: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: db, Layouts: layouts{}}
	if err := h.Deployed(ctx, env, c, it); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Packages().Find(ctx, c.Key, "com.acme:app"); err != nil {
		t.Fatalf("indexed under no layout: %v", err)
	}
	for _, it := range []types.Item{{Folder: true, Name: "com"}, {Name: "readme.txt"}} {
		if err := h.Deployed(ctx, env, c, it); err != nil {
			t.Fatal(err)
		}
	}
	broken := format.Env{Store: db, Layouts: layouts{fail: true}}
	for name, err := range map[string]error{
		"deployed": h.Deployed(ctx, broken, c, it),
		"deleted":  h.Deleted(ctx, broken, c, []types.Item{it}),
		"reindex":  h.Reindex(ctx, broken, c),
	} {
		if !errors.Is(err, errLayout) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := h.Deleted(ctx, env, c, []types.Item{{Folder: true, Name: "com"}}); err != nil {
		t.Fatal(err)
	}
	undefined := c
	undefined.RepoLayoutRef = "no-such-layout"
	if err := errors.Join(
		h.Deployed(ctx, env, undefined, it),
		h.Deleted(ctx, env, undefined, []types.Item{it}),
		h.Reindex(ctx, env, undefined),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Packages().
		Find(ctx, c.Key, "com.acme:app"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("a reindex under an undefined layout kept the index: %v", err)
	}
}

var errIndex = errors.New("index boom")

// faulty fails the package and item store methods fail names and
// delegates the rest.
type faulty struct {
	store.Store
	fail map[string]bool
}

func (f faulty) Packages() store.PackageStore { return faultyPackages{f.Store.Packages(), f.fail} }
func (f faulty) Items() store.ItemStore       { return faultyItems{f.Store.Items(), f.fail} }

type faultyPackages struct {
	store.PackageStore
	fail map[string]bool
}

type faultyItems struct {
	store.ItemStore
	fail map[string]bool
}

//nolint:wrapcheck // test double
func (i faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if i.fail["Search"] {
		return nil, errIndex
	}
	return i.ItemStore.Search(ctx, q, o)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Ensure(ctx context.Context, v types.Package) (types.Package, error) {
	if p.fail["Ensure"] {
		return types.Package{}, errIndex
	}
	return p.PackageStore.Ensure(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.fail["Find"] {
		return types.Package{}, errIndex
	}
	return p.PackageStore.Find(ctx, key, name)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Version(
	ctx context.Context,
	id int64,
	v string,
) (types.PackageVersion, error) {
	if p.fail["Version"] {
		return types.PackageVersion{}, errIndex
	}
	return p.PackageStore.Version(ctx, id, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if p.fail["Versions"] {
		return nil, errIndex
	}
	return p.PackageStore.Versions(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) DeleteVersion(ctx context.Context, id int64) error {
	if p.fail["DeleteVersion"] {
		return errIndex
	}
	return p.PackageStore.DeleteVersion(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Delete(ctx context.Context, id int64) error {
	if p.fail["Delete"] {
		return errIndex
	}
	return p.PackageStore.Delete(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) List(
	ctx context.Context, key string, pg store.Page,
) ([]types.Package, string, error) {
	if p.fail["List"] {
		return nil, "", errIndex
	}
	return p.PackageStore.List(ctx, key, pg)
}

// Every store failure in the hooks and the reindex comes back to the
// caller, which rolls its transaction back.
func TestIndexFailures(t *testing.T) {
	h := maven.Handler{}
	const (
		jar = "com/acme/app/1.0/app-1.0.jar"
		pom = "com/acme/app/1.1/app-1.1.pom"
	)
	// gone removes the jar from the tree without the hooks, leaving 1.0
	// indexed with nothing behind it; with 1.1 also gone, the package has no
	// version the tree backs.
	gone := func(w *world, also ...string) []types.Item {
		var out []types.Item
		for _, p := range append([]string{jar}, also...) {
			it, err := w.db.Items().Get(w.ctx, w.c.Key, false, p)
			if err != nil {
				w.t.Fatal(err)
			}
			if _, err := w.db.Items().Delete(w.ctx, w.c.Key, false, p); err != nil {
				w.t.Fatal(err)
			}
			out = append(out, it)
		}
		return out
	}
	deployed := func(w *world, st store.Store) error {
		it, err := w.db.Items().Get(w.ctx, w.c.Key, false, jar)
		if err != nil {
			w.t.Fatal(err)
		}
		return h.Deployed(w.ctx, format.Env{Store: st, Layouts: layouts{}}, w.c, it)
	}
	deleted := func(also ...string) func(w *world, st store.Store) error {
		return func(w *world, st store.Store) error {
			removed := gone(w, also...)
			return h.Deleted(w.ctx, format.Env{Store: st, Layouts: layouts{}}, w.c, removed)
		}
	}
	reindex := func(w *world, st store.Store) error {
		return h.Reindex(w.ctx, format.Env{Store: st, Layouts: layouts{}}, w.c)
	}
	for _, tc := range []struct {
		name string
		fail string
		run  func(*world, store.Store) error
	}{
		{"deployed: search", "Search", deployed},
		{"deployed: ensure", "Ensure", deployed},
		{"deployed: sync", "Version", deployed},
		{"deleted: find", "Find", deleted()},
		{"deleted: version", "Version", deleted()},
		{"deleted: drop version", "DeleteVersion", deleted()},
		{"deleted: versions", "Versions", deleted()},
		{"deleted: drop package", "Delete", deleted(pom)},
		{"reindex: search", "Search", reindex},
		{"reindex: reconcile", "List", reindex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil)
			w.deploy(jar, pom)
			if err := tc.run(
				w,
				faulty{w.db, map[string]bool{tc.fail: true}},
			); !errors.Is(
				err,
				errIndex,
			) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A removal whose version the index no longer holds, or whose package it
// never held, settles nothing and fails nothing.
func TestDeletedUnindexed(t *testing.T) {
	w := newWorld(t, nil)
	w.deploy("com/acme/app/1.1/app-1.1.jar")
	h := maven.Handler{}
	env := format.Env{Store: w.db, Layouts: layouts{}}
	removed := []types.Item{
		{RepoKey: w.c.Key, Path: "com/acme/app/1.0", Name: "app-1.0.jar"},
		{RepoKey: w.c.Key, Path: "com/acme/other/1.0", Name: "other-1.0.jar"},
	}
	if err := h.Deleted(w.ctx, env, w.c, removed); err != nil {
		t.Fatal(err)
	}
	if got := w.versions("com.acme:app"); len(got) != 1 || got[0].Version != "1.1" {
		t.Fatalf("%+v", got)
	}
}
