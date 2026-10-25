package nuget_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("store fault")

// faults says which store methods fail: each armed method lets its count
// of calls through, then fails every call after.
type faults struct {
	pass map[string]int
}

func (f *faults) fail(method string) bool {
	n, ok := f.pass[method]
	if !ok {
		return false
	}
	if n > 0 {
		f.pass[method] = n - 1
		return false
	}
	return true
}

// faulty fails the package, item and repository store methods its faults
// arm and delegates the rest, transactions included.
type faulty struct {
	store.Store
	f *faults
}

func (f *faulty) Packages() store.PackageStore { return faultyPackages{f.Store.Packages(), f.f} }

func (f *faulty) Items() store.ItemStore { return faultyItems{f.Store.Items(), f.f} }

func (f *faulty) Repos() store.RepoStore { return faultyRepos{f.Store.Repos(), f.f} }

func (f *faulty) WorkQueue() store.WorkQueue { return faultyQueue{f.Store.WorkQueue(), f.f} }

type faultyQueue struct {
	store.WorkQueue
	f *faults
}

//nolint:wrapcheck // test double
func (q faultyQueue) Enqueue(
	ctx context.Context, kind, key string, payload []byte, now time.Time,
) error {
	if q.f.fail("Enqueue") {
		return errFault
	}
	return q.WorkQueue.Enqueue(ctx, kind, key, payload, now)
}

type faultyRepos struct {
	store.RepoStore
	f *faults
}

//nolint:wrapcheck // test double
func (r faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.f.fail("RepoGet") {
		return repo.Config{}, errFault
	}
	return r.RepoStore.Get(ctx, key)
}

type faultyPackages struct {
	store.PackageStore
	f *faults
}

//nolint:wrapcheck // test double
func (p faultyPackages) Ensure(ctx context.Context, v types.Package) (types.Package, error) {
	if p.f.fail("Ensure") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Ensure(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Get(ctx context.Context, id int64) (types.Package, error) {
	if p.f.fail("PackageGet") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Get(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.f.fail("Find") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Find(ctx, key, name)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Version(
	ctx context.Context, id int64, version string,
) (types.PackageVersion, error) {
	if p.f.fail("Version") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.Version(ctx, id, version)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if p.f.fail("Versions") {
		return nil, errFault
	}
	return p.PackageStore.Versions(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) VersionOf(ctx context.Context, id int64) (types.PackageVersion, error) {
	if p.f.fail("VersionOf") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.VersionOf(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) PutVersion(
	ctx context.Context, v types.PackageVersion,
) (types.PackageVersion, error) {
	if p.f.fail("PutVersion") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.PutVersion(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) DeleteVersion(ctx context.Context, id int64) error {
	if p.f.fail("DeleteVersion") {
		return errFault
	}
	return p.PackageStore.DeleteVersion(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Delete(ctx context.Context, id int64) error {
	if p.f.fail("Delete") {
		return errFault
	}
	return p.PackageStore.Delete(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if p.f.fail("Files") {
		return nil, errFault
	}
	return p.PackageStore.Files(ctx, id)
}

type faultyItems struct {
	store.ItemStore
	f *faults
}

//nolint:wrapcheck // test double
func (i faultyItems) GetByID(ctx context.Context, id int64) (types.Item, error) {
	if i.f.fail("GetByID") {
		return types.Item{}, errFault
	}
	return i.ItemStore.GetByID(ctx, id)
}

//nolint:wrapcheck // test double
func (i faultyItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if i.f.fail("Properties") {
		return nil, errFault
	}
	return i.ItemStore.Properties(ctx, id)
}

//nolint:wrapcheck // test double
func (i faultyItems) PatchProperties(
	ctx context.Context, id int64, set types.Properties, remove []string, at time.Time,
) error {
	if i.f.fail("PatchProperties") {
		return errFault
	}
	return i.ItemStore.PatchProperties(ctx, id, set, remove, at)
}

//nolint:wrapcheck // test double
func (i faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if i.f.fail("Search") {
		return nil, errFault
	}
	return i.ItemStore.Search(ctx, q, o)
}

// Fixtures the faults run against.
var (
	fooPkg = "Foo.1.0.0-rc.1.nupkg"
	fooSym = "Foo.1.0.0-rc.1.snupkg"
)

func withPackage(w *world) { w.deploy(fooPkg, pkg("Foo", "1.0.0-rc.1", "<title>T</title>"), nil) }

func withBoth(w *world) {
	withPackage(w)
	w.deploy(fooSym, symbols("Foo", "1.0.0-rc.1"), nil)
}

func withSnapshots(w *world) {
	for _, v := range []string{"1.0.0-a", "1.0.0-b", "1.0.0-c"} {
		w.deploy("Foo."+v+".nupkg", pkg("Foo", v, ""), nil)
	}
}

func deployPackage(w *world) error {
	_, err := w.tryDeploy(w.c, fooPkg, pkg("Foo", "1.0.0-rc.1", "<title>T</title>"), nil)
	return err
}

func deploySymbols(w *world) error {
	_, err := w.tryDeploy(w.c, fooSym, symbols("Foo", "1.0.0-rc.1"), nil)
	return err
}

func deployElsewhere(w *world) error {
	_, err := w.tryDeploy(w.c, "x/"+fooPkg, pkg("Foo", "1.0.0-rc.1", ""), nil)
	return err
}

func removePackage(w *world) error {
	_, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: fooPkg}, "tester")
	return err //nolint:wrapcheck // test helper
}

func reindexAll(w *world) error {
	return w.items.Reindex(w.ctx, w.c) //nolint:wrapcheck // test helper
}

// sweepAll runs the cleanup of Foo straight through the format, since the
// queue keeps a run's error to retry it rather than returning it.
func sweepAll(w *world) error {
	return nuget.Handler{}.Work(w.ctx, w.items.Env(), //nolint:wrapcheck // test helper
		types.WorkItem{Kind: nuget.KindSnapshots, Key: w.c.Key + "/foo"})
}

// Every store failure the format meets fails the write it runs in, so the
// write rolls back rather than leaving the index behind the tree; and a
// failure in a reindex or a cleanup fails it, so the work queue retries.
func TestUnitStoreFaults(t *testing.T) {
	type setup func(w *world)
	for _, c := range []struct {
		name  string
		fail  string
		pass  int
		limit bool
		setup setup
		run   func(w *world) error
	}{
		{"deploy reads properties", "Properties", 0, false, nil, deployPackage},
		{"deploy sets properties", "PatchProperties", 0, false, nil, deployPackage},
		// Admit's duplicate check reads the index first, so the hook's own
		// reads come after the calls it lets through.
		{"deploy finds the package", "Find", 1, false, nil, deployPackage},
		{"deploy creates the package", "Ensure", 0, false, nil, deployPackage},
		{"deploy creates the version", "PutVersion", 0, false, nil, deployPackage},
		{"deploy finds its old version", "VersionOf", 0, false, nil, deployPackage},
		{"deploy finds the version", "Version", 1, false, withPackage, deploySymbols},
		{"deploy reads the version's files", "Files", 1, false, withPackage, deploySymbols},
		{"deploy reads a version's file", "GetByID", 1, false, withPackage, deploySymbols},
		{"overwrite finds its version's package", "PackageGet", 0, false, withPackage, deployPackage},
		{"deploy marks the cleanup", "Enqueue", 0, true, nil, deployPackage},
		{"admit finds the package", "Find", 0, false, withPackage, deployElsewhere},
		{"admit finds the version", "Version", 0, false, withPackage, deployElsewhere},
		{"admit reads the files", "Files", 0, false, withPackage, deployElsewhere},
		{"admit reads a file", "GetByID", 0, false, withPackage, deployElsewhere},
		{"delete finds the package", "Find", 0, false, withBoth, removePackage},
		{"delete finds the version", "Version", 0, false, withBoth, removePackage},
		{"delete reads what is left", "Files", 1, false, withBoth, removePackage},
		{"reindex lists the packages", "Search", 0, false, withBoth, reindexAll},
		{"reindex lists them again", "Search", 1, false, withBoth, reindexAll},
		// The annotation writes its own properties first.
		{"reindex sets properties", "PatchProperties", 1, false, withBoth, func(w *world) error {
			if err := w.items.Annotate(w.ctx, w.c, w.item(fooPkg), nil,
				[]string{nuget.PropTitle}); err != nil {
				return err //nolint:wrapcheck // test helper
			}
			return reindexAll(w)
		}},
		{"reindex reconciles", "PutVersion", 0, false, withBoth, func(w *world) error {
			if err := wipe(w); err != nil {
				return err
			}
			return reindexAll(w)
		}},
		{"cleanup reads the repository", "RepoGet", 0, true, withSnapshots, sweepAll},
		{"cleanup finds the package", "Find", 0, true, withSnapshots, sweepAll},
		{"cleanup reads the versions", "Versions", 0, true, withSnapshots, sweepAll},
		{"cleanup reads the files", "Files", 0, true, withSnapshots, sweepAll},
		{"cleanup reads a file", "GetByID", 0, true, withSnapshots, sweepAll},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &faults{pass: map[string]int{}}
			var tune func(*repo.Config)
			if c.limit {
				tune = limited(2)
			}
			w := newWorldOn(t, &faulty{Store: inmem.New(), f: f}, tune)
			if c.setup != nil {
				c.setup(w)
			}
			f.pass[c.fail] = c.pass
			if err := c.run(w); !errors.Is(err, errFault) {
				t.Fatalf("%v", err)
			}
		})
	}
}

// wipe removes the repository's index.
func wipe(w *world) error {
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		return err //nolint:wrapcheck // test helper
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			return err //nolint:wrapcheck // test helper
		}
	}
	return nil
}

// A version whose last file leaves it goes, and a failure to drop it, or
// its package, fails the write; one whose stored Metadata does not decode
// is described anew from its package.
func TestUnitDropFaults(t *testing.T) {
	for _, fail := range []string{"DeleteVersion", "Versions", "Delete"} {
		f := &faults{pass: map[string]int{}}
		w := newWorldOn(t, &faulty{Store: inmem.New(), f: f}, nil)
		r := w.repo("nuget-remote", repo.Remote, nil)
		cache := func(p string, body []byte) error {
			_, err := w.items.Deploy(w.ctx, item.Deploy{
				Target: item.Target{Repo: r, Cached: true, Path: p}, Body: bytes.NewReader(body),
			})
			return err //nolint:wrapcheck // test helper
		}
		if err := cache(fooPkg, pkg("Foo", "1.0.0-rc.1", "")); err != nil {
			t.Fatal(err)
		}
		f.pass[fail] = 0
		if err := cache(fooPkg, []byte("garbage")); !errors.Is(err, errFault) {
			t.Fatalf("%s: %v", fail, err)
		}
	}
	w := newWorld(t, nil)
	withPackage(w)
	p, err := w.st.Packages().Find(w.ctx, w.c.Key, "foo")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := w.st.Packages().Version(w.ctx, p.ID, "1.0.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	pv.Metadata = []byte("{")
	if _, err := w.st.Packages().PutVersion(w.ctx, pv); err != nil {
		t.Fatal(err)
	}
	w.deploy(fooSym, symbols("Foo", "1.0.0-rc.1"), nil)
	if v := w.version(
		"foo",
		"1.0.0-rc.1",
	); v.Metadata.Title != "T" ||
		v.Metadata.Symbols != fooSym {
		t.Fatalf("%+v", v)
	}
	// A cleanup over Metadata that does not decode fails, rather than
	// guessing which versions are snapshots.
	lim := newWorld(t, limited(2))
	withSnapshots(lim)
	p, _ = lim.st.Packages().Find(lim.ctx, lim.c.Key, "foo")
	pv, _ = lim.st.Packages().Version(lim.ctx, p.ID, "1.0.0-a")
	pv.Metadata = []byte("{")
	if _, err := lim.st.Packages().PutVersion(lim.ctx, pv); err != nil {
		t.Fatal(err)
	}
	if err := sweepAll(lim); err == nil {
		t.Fatal("cleaned over corrupt metadata")
	}
}

// A blob store that fails is a failure, not content that is no package: the
// write fails rather than being refused as invalid.
func TestUnitBlobFaults(t *testing.T) {
	h := nuget.Handler{}
	env := format.Env{Blobs: failingBlobs{body: pkg("Foo", "1.0", "")}}
	_, err := h.Inspect(t.Context(), env, repo.Config{}, format.Write{
		Path: "Foo.1.0.0.nupkg", Checksums: types.Checksums{SHA256: "x"},
	})
	if !errors.Is(err, errBlob) {
		t.Fatalf("%v", err)
	}
	// A restore inspects nothing, so its hook reads the content itself.
	w := newWorld(t, nil)
	withPackage(w)
	env = w.items.Env()
	env.Blobs = failingBlobs{}
	if err := h.Deployed(w.ctx, env, w.c, w.item(fooPkg)); !errors.Is(err, errBlob) {
		t.Fatalf("%v", err)
	}
	// A linked file whose content the store has lost fails the version's
	// next change.
	w.deploy("x/"+fooSym, symbols("Foo", "1.0.0-rc.1"), nil)
	env.Inspection = nil
	sym := w.item("x/" + fooSym)
	if err := h.Deleted(w.ctx, env, w.c, []types.Item{sym}); err != nil {
		t.Fatal(err)
	}
	p, _ := w.st.Packages().Find(w.ctx, w.c.Key, "foo")
	pv, _ := w.st.Packages().Version(w.ctx, p.ID, "1.0.0-rc.1")
	pv.Metadata = []byte("{}")
	if _, err := w.st.Packages().PutVersion(w.ctx, pv); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(w.ctx, env, w.c, []types.Item{sym}); !errors.Is(err, errBlob) {
		t.Fatalf("%v", err)
	}
	if err := h.Reindex(w.ctx, env, w.c); !errors.Is(err, errBlob) {
		t.Fatalf("%v", err)
	}
}
