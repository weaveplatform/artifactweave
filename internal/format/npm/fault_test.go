package npm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/pkg/aql"
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

// faulty fails the package and item store methods its faults arm and
// delegates the rest, transactions included.
type faulty struct {
	store.Store
	f *faults
}

func (f *faulty) Packages() store.PackageStore { return faultyPackages{f.Store.Packages(), f.f} }

func (f *faulty) Items() store.ItemStore { return faultyItems{f.Store.Items(), f.f} }

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

// Every store failure the format meets fails the write it runs in, so the
// write rolls back rather than leaving the index behind the tree.
func TestStoreFaults(t *testing.T) {
	p1, p2 := "left-pad/-/left-pad-1.0.0.tgz", "left-pad/-/left-pad-2.0.0.tgz"
	type setup func(w *world)
	two := func(w *world) {
		w.deploy(
			p1,
			pkg("left-pad", "1.0.0", ""),
			types.Properties{npm.PropDistTag: {"beta", "rc"}},
		)
		w.deploy(p2, pkg("left-pad", "2.0.0", ""), nil)
	}
	one := func(w *world) { w.deploy(p1, pkg("left-pad", "1.0.0", ""), nil) }
	// pass counts the calls the item service makes itself before the
	// format's: an annotation writes its own properties first.
	for _, c := range []struct {
		name  string
		fail  string
		pass  int
		setup setup
		run   func(w *world) error
	}{
		{"deploy reads properties", "Properties", 0, nil, deployOne},
		{"deploy sets properties", "PatchProperties", 0, nil, deployOne},
		{"deploy creates the package", "Ensure", 0, nil, deployOne},
		{"deploy finds the old version", "VersionOf", 0, one, overwriteGarbage},
		{"overwrite drops the version", "DeleteVersion", 0, one, overwriteGarbage},
		{"overwrite reads what is left", "Versions", 0, one, overwriteGarbage},
		{"overwrite drops the package", "Delete", 0, one, overwriteGarbage},
		{"annotate finds the version", "VersionOf", 0, one, tag},
		{"annotate reads the versions", "Versions", 0, two, tag},
		{"annotate reads the files", "Files", 0, two, tag},
		{"annotate drops tags", "PatchProperties", 1, two, tag},
		{"annotate keeps other tags", "PatchProperties", 1, two, tagBeta},
		{"annotate reads the tags it drops", "Properties", 0, two, tag},
		{"annotate reads properties", "Properties", 0, one, tag},
		{"annotate updates the version", "PutVersion", 0, one, tag},
		{"reindex lists tarballs", "Search", 0, one, reindex},
		{"reindex lists them again", "Search", 1, one, reindex},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &faults{pass: map[string]int{}}
			w := newWorldOn(t, &faulty{Store: inmem.New(), f: f})
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

func deployOne(w *world) error {
	_, err := w.tryDeploy("left-pad/-/left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), nil)
	return err
}

func overwriteGarbage(w *world) error {
	_, err := w.tryDeploy("left-pad/-/left-pad-1.0.0.tgz", []byte("garbage"), nil)
	return err
}

// tag gives 2.0.0 the tags 1.0.0 holds, or 1.0.0 a tag when it is alone.
func tag(w *world) error {
	p := "left-pad/-/left-pad-2.0.0.tgz"
	if _, err := w.st.Items().Get(w.ctx, w.c.Key, false, p); err != nil {
		p = "left-pad/-/left-pad-1.0.0.tgz"
	}
	return annotateErr(w, p, types.Properties{npm.PropDistTag: {"beta", "rc"}})
}

// tagBeta gives 2.0.0 beta alone, so 1.0.0 keeps rc.
func tagBeta(w *world) error {
	return annotateErr(
		w,
		"left-pad/-/left-pad-2.0.0.tgz",
		types.Properties{npm.PropDistTag: {"beta"}},
	)
}

func annotateErr(w *world, p string, set types.Properties) error {
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		return err //nolint:wrapcheck // test helper
	}
	return w.items.Annotate(w.ctx, w.c, it, set, nil) //nolint:wrapcheck // test helper
}

func reindex(w *world) error {
	return w.items.Reindex(w.ctx, w.c) //nolint:wrapcheck // test helper
}

// A version whose stored Metadata does not decode fails the property write
// that would rewrite it, rather than replacing it with a guess; a reindex
// rebuilds it.
func TestCorruptMetadata(t *testing.T) {
	w := newWorld(t)
	p1, p2 := "left-pad/-/left-pad-1.0.0.tgz", "left-pad/-/left-pad-2.0.0.tgz"
	w.deploy(p1, pkg("left-pad", "1.0.0", ""), types.Properties{npm.PropDistTag: {"beta"}})
	w.deploy(p2, pkg("left-pad", "2.0.0", ""), nil)
	p, err := w.st.Packages().Find(w.ctx, w.c.Key, "left-pad")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		pv, err := w.st.Packages().Version(w.ctx, p.ID, v)
		if err != nil {
			t.Fatal(err)
		}
		pv.Metadata = []byte("{")
		if _, err := w.st.Packages().PutVersion(w.ctx, pv); err != nil {
			t.Fatal(err)
		}
	}
	for _, set := range []types.Properties{
		{npm.PropDistTag: {"beta"}}, {npm.PropDeprecated: {"x"}},
	} {
		if err := annotateErr(w, p2, set); err == nil {
			t.Fatalf("%v: annotated over corrupt metadata", set)
		}
	}
	if err := reindex(w); err != nil {
		t.Fatal(err)
	}
	if got := w.tags("left-pad"); len(got["1.0.0"]) != 1 {
		t.Fatalf("reindexed %v", got)
	}
}
