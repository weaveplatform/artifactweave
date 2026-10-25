package pypi_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("injected fault")

// faults arms one method to fail once it has let pass calls through.
type faults struct {
	method string
	pass   int
}

func (f *faults) fail(method string) bool {
	if f.method != method {
		return false
	}
	if f.pass > 0 {
		f.pass--
		return false
	}
	return true
}

// faulty fails the package and item store methods its faults arm and
// delegates the rest, transactions included.
type faulty struct {
	store.Store
	f *faults
}

func (s *faulty) Packages() store.PackageStore { return faultyPackages{s.Store.Packages(), s.f} }

func (s *faulty) Items() store.ItemStore { return faultyItems{s.Store.Items(), s.f} }

type faultyPackages struct {
	store.PackageStore
	f *faults
}

//nolint:wrapcheck // test double
func (p faultyPackages) Ensure(ctx context.Context, v types.Package) (types.Package, error) {
	if p.f.fail("Ensure") {
		return v, errFault
	}
	return p.PackageStore.Ensure(ctx, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Get(ctx context.Context, id int64) (types.Package, error) {
	if p.f.fail("Get") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Get(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Version(
	ctx context.Context,
	id int64,
	v string,
) (types.PackageVersion, error) {
	if p.f.fail("Version") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.Version(ctx, id, v)
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
	ctx context.Context,
	v types.PackageVersion,
) (types.PackageVersion, error) {
	if p.f.fail("PutVersion") {
		return v, errFault
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
	ctx context.Context,
	id int64,
	set types.Properties,
	remove []string,
	at time.Time,
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

// faultyBlobs fails opening content ("Open"), or reading it ("Read"),
// once its faults let pass calls through.
type faultyBlobs struct {
	blob.Store
	f *faults
}

//nolint:wrapcheck // test double
func (b faultyBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if b.f.fail("Open") {
		return nil, blob.Info{}, errFault
	}
	rc, info, err := b.Store.Open(ctx, sha)
	if err == nil && b.f.fail("Read") {
		return failingReader{rc}, info, nil
	}
	return rc, info, err
}

// failingReader fails every read.
type failingReader struct{ io.ReadSeekCloser }

func (failingReader) Read([]byte) (int, error) { return 0, errFault }

// scenario drives every hook through every way it changes the index:
// deploys, an overwrite into another project, files indexed by their
// properties, yanking, deletes, a trash restore and a reindex. It stops at
// the first failure.
func scenario(w *world) error {
	whl, tgz := "x/1.0/"+wheelName("x", "1.0"), "x/1.0/x-1.0.tar.gz"
	x, y := meta("x", "1.0", "Summary: x"), meta("y", "1.0")
	bin := trash.Service{Store: w.st, Items: w.items, Now: func() time.Time { return w.now }}
	step := func(p string, body []byte, props types.Properties) func() error {
		return func() error {
			_, err := w.tryDeploy(w.c, p, body, props)
			return err
		}
	}
	annotate := func(p string, set types.Properties, remove ...string) func() error {
		return func() error {
			it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
			if err != nil {
				return err //nolint:wrapcheck // test helper
			}
			return w.items.Annotate(w.ctx, w.c, it, set, remove) //nolint:wrapcheck // test helper
		}
	}
	remove := func(p string) func() error {
		return func() error {
			_, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "tester")
			return err //nolint:wrapcheck // test helper
		}
	}
	for _, s := range []func() error{
		step(whl, wheel("x", "1.0", x), types.Properties{pypi.PropYanked: {"true"}}),
		step(tgz, sdist("x", "1.0", x), nil),
		step("y/"+wheelName("x", "1.0"), wheel("x", "1.0", x), nil),
		annotate(whl, types.Properties{pypi.PropYanked: {"reason"}}),
		step(tgz, sdist("y", "1.0", y), types.Properties{pypi.PropSummary: {"stale"}}),
		step("m/m.zip", []byte("x"), types.Properties{
			pypi.PropName: {"m"}, pypi.PropVersion: {"1"},
		}),
		annotate("m/m.zip", types.Properties{pypi.PropVersion: {"2"}}),
		annotate("m/m.zip", nil, pypi.PropName),
		step(tgz, []byte("unreadable"), nil),
		remove("y/" + wheelName("x", "1.0")),
		remove(whl),
		func() error {
			_, err := bin.Restore(w.ctx, w.c.Key, whl, trash.Destination{Repo: w.c}, "tester", nil)
			return err //nolint:wrapcheck // test helper
		},
		func() error { return w.items.Reindex(w.ctx, w.c) }, //nolint:wrapcheck // test helper
	} {
		w.now = w.now.Add(time.Minute)
		if err := s(); err != nil {
			return err
		}
	}
	return nil
}

// Every store or blob failure the format meets fails the write it runs
// in, so the write rolls back rather than leaving the index behind the
// tree: the scenario fails each method at each of its calls in turn, and
// either completes or fails with the fault.
func TestUnitFaults(t *testing.T) {
	methods := []string{
		"Ensure", "Get", "Version", "Versions", "VersionOf", "PutVersion", "DeleteVersion",
		"Delete", "Files", "GetByID", "Properties", "PatchProperties", "Search", "Open", "Read",
	}
	for _, m := range methods {
		failed := false
		for pass := 0; ; pass++ {
			f := &faults{method: m, pass: pass}
			w := newWorldOn(t, &faulty{Store: inmem.New(), f: f}, pypi.Settings{})
			w.items.Blobs = faultyBlobs{Store: w.items.Blobs, f: f}
			err := scenario(w)
			if err == nil {
				break
			}
			if !errors.Is(err, errFault) {
				t.Fatalf("%s after %d calls: %v", m, pass, err)
			}
			failed = true
		}
		if !failed {
			t.Errorf("the scenario never calls %s", m)
		}
	}
}

// corrupt replaces the Metadata of version v of project p with b.
func (w *world) corrupt(p, v string, b []byte) {
	w.t.Helper()
	pkg, err := w.st.Packages().Find(w.ctx, w.c.Key, p)
	if err != nil {
		w.t.Fatal(err)
	}
	pv, err := w.st.Packages().Version(w.ctx, pkg.ID, v)
	if err != nil {
		w.t.Fatal(err)
	}
	pv.Metadata = b
	if _, err := w.st.Packages().PutVersion(w.ctx, pv); err != nil {
		w.t.Fatal(err)
	}
}

// A version whose stored Metadata does not decode fails every write that
// would rewrite it, rather than replacing it with a guess; a reindex
// rebuilds it.
func TestUnitCorruptMetadata(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	whl, tgz := "x/"+wheelName("x", "1.0"), "x/x-1.0.tar.gz"
	w.deploy(whl, wheel("x", "1.0", meta("x", "1.0")), nil)
	w.deploy(tgz, sdist("x", "1.0", meta("x", "1.0")), nil)
	w.corrupt("x", "1.0", []byte("{"))
	for what, write := range map[string]func() error{
		"another file": func() error {
			_, err := w.tryDeploy(w.c, "x/x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0")}), nil)
			return err
		},
		"a yank": func() error {
			return w.items.Annotate(w.ctx, w.c, w.item(whl), //nolint:wrapcheck // test
				types.Properties{pypi.PropYanked: {"true"}}, nil)
		},
		"a delete": func() error {
			_, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: tgz}, "tester")
			return err //nolint:wrapcheck // test
		},
		"an overwrite into another version": func() error {
			_, err := w.tryDeploy(w.c, tgz, sdist("x", "2.0", meta("x", "2.0")), nil)
			return err
		},
	} {
		if err := write(); err == nil {
			t.Errorf("%s over corrupt metadata", what)
		}
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	if got := paths(w.index()["x"]["1.0"]); len(got) != 2 {
		t.Fatalf("reindexed %v", got)
	}
	// A version that lost a file's record but keeps its link: a yank
	// indexes the file again by the properties its deploy set, and a
	// reindex brings its core metadata back.
	w.corrupt("x", "1.0", []byte(`{"files":[]}`))
	w.annotate(whl, types.Properties{pypi.PropYanked: {"true"}})
	if m := w.index()["x"]["1.0"]; len(m.Files) != 1 || !m.Files[0].Yanked ||
		m.Files[0].Metadata != "" {
		t.Fatalf("after the yank: %+v", m)
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	if m := w.index()["x"]["1.0"]; len(m.Files) != 2 || m.Files[0].Metadata == "" {
		t.Fatalf("after the reindex: %+v", m)
	}
}

// changing is a store whose files all hold other content by the time a
// reindex's transaction reads them again, as a deploy in between leaves
// them.
type changing struct {
	store.Store
	searches *int
}

func (s changing) Items() store.ItemStore { return changingItems{s.Store.Items(), s.searches} }

type changingItems struct {
	store.ItemStore
	searches *int
}

//nolint:wrapcheck // test double
func (i changingItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	hits, err := i.ItemStore.Search(ctx, q, o)
	if *i.searches++; *i.searches > 1 {
		for n := range hits {
			hits[n].Item.Actual.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}
	}
	return hits, err
}

// Content that changed after a reindex's first reading is read inside its
// transaction, and a failure to read it fails the reindex.
func TestUnitReindexRereadsChangedContent(t *testing.T) {
	searches := 0
	w := newWorldOn(t, changing{Store: inmem.New(), searches: &searches}, pypi.Settings{})
	w.deploy("x/x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "1.0")}), nil)
	searches = 0
	if err := w.items.Reindex(w.ctx, w.c); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
