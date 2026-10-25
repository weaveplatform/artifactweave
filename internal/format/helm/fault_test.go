package helm_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("store fault")

// faults arms one method: it lets pass calls through, then fails every
// call after, until disarmed.
type faults struct {
	method string
	pass   int
	hit    bool
}

func (f *faults) fail(method string) bool {
	if f.method != method {
		return false
	}
	if f.pass > 0 {
		f.pass--
		return false
	}
	f.hit = true
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
		return types.Package{}, errFault
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
func (p faultyPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.f.fail("Find") {
		return types.Package{}, errFault
	}
	return p.PackageStore.Find(ctx, key, name)
}

//nolint:wrapcheck // test double
func (p faultyPackages) List(
	ctx context.Context,
	key string,
	pg store.Page,
) ([]types.Package, string, error) {
	if p.f.fail("List") {
		return nil, "", errFault
	}
	return p.PackageStore.List(ctx, key, pg)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Delete(ctx context.Context, id int64) error {
	if p.f.fail("Delete") {
		return errFault
	}
	return p.PackageStore.Delete(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) PutVersion(
	ctx context.Context,
	v types.PackageVersion,
) (types.PackageVersion, error) {
	if p.f.fail("PutVersion") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.PutVersion(ctx, v)
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
func (p faultyPackages) DeleteVersion(ctx context.Context, id int64) error {
	if p.f.fail("DeleteVersion") {
		return errFault
	}
	return p.PackageStore.DeleteVersion(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) LinkFile(ctx context.Context, f types.VersionFile) error {
	if p.f.fail("LinkFile") {
		return errFault
	}
	return p.PackageStore.LinkFile(ctx, f)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if p.f.fail("Files") {
		return nil, errFault
	}
	return p.PackageStore.Files(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) VersionOf(ctx context.Context, id int64) (types.PackageVersion, error) {
	if p.f.fail("VersionOf") {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.VersionOf(ctx, id)
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

// faultyBlobs fails Open once armed, as a blob store that loses content
// between writes would.
type faultyBlobs struct {
	blob.Store
	f *faults
}

//nolint:wrapcheck // test double
func (b faultyBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if b.f.fail("Open") {
		return nil, blob.Info{}, errFault
	}
	return b.Store.Open(ctx, sha)
}

// faultMethods are the store calls the format makes.
var faultMethods = []string{
	"Ensure", "Get", "Find", "List", "Delete", "PutVersion", "Version", "Versions",
	"DeleteVersion", "LinkFile", "Files", "VersionOf", "GetByID", "Properties",
	"PatchProperties", "Search", "Open",
}

// Every store failure the format meets fails the write or the reindex it
// runs in, which rolls back: the index is as it was before. Each method is
// failed at its first call, then its second, and so on, until the operation
// no longer reaches a failing call.
func TestUnitStoreFaults(t *testing.T) {
	dup := func(w *world) {
		w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: a"))
		w.deploy("b/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: b"))
	}
	for _, c := range []struct {
		name  string
		setup func(w *world)
		run   func(w *world) error
	}{
		{"deploy", nil, func(w *world) error {
			_, err := w.deployTo(w.c, "web-1.0.0.tgz", chartTgz("web", "1.0.0", "appVersion: 1"))
			return err
		}},
		{"deploy a duplicate", dup, func(w *world) error {
			_, err := w.deployTo(w.c, "c/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
			return err
		}},
		{"overwrite the newest with another chart", dup, func(w *world) error {
			_, err := w.deployTo(w.c, "b/web-1.0.0.tgz", chartTgz("api", "2.0.0"))
			return err
		}},
		{"overwrite the last with no chart", func(w *world) {
			w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
		}, func(w *world) error {
			_, err := w.deployTo(w.c, "a/web-1.0.0.tgz", []byte("junk"))
			return err
		}},
		{"delete the newest", dup, func(w *world) error {
			_, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: "b/web-1.0.0.tgz"}, "t")
			return err //nolint:wrapcheck // test helper
		}},
		{"reindex", dup, func(w *world) error { return w.items.Reindex(w.ctx, w.c) }},
		{"reindex a path", dup, func(w *world) error { return w.items.ReindexPath(w.ctx, w.c, "a") }},
		{"reindex a path after a wipe", func(w *world) {
			dup(w)
			w.wipe()
		}, func(w *world) error { return w.items.ReindexPath(w.ctx, w.c, "b") }},
		{"deploy a duplicate where they are refused", func(w *world) {
			yes := true
			w.c = w.repo("helm-unique", func(c *repo.Config) { c.ForceNonDuplicateChart = &yes })
			w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
		}, func(w *world) error {
			_, err := w.deployTo(w.c, "a/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: again"))
			return err
		}},
	} {
		for _, m := range faultMethods {
			for pass := 0; ; pass++ {
				f := &faults{}
				w := newWorldOn(t, &faulty{Store: inmem.New(), f: f})
				w.items.Blobs = faultyBlobs{Store: w.items.Blobs, f: f}
				if c.setup != nil {
					c.setup(w)
				}
				before := w.index(w.c)
				f.method, f.pass = m, pass
				err := c.run(w)
				f.method = ""
				if !f.hit {
					if err != nil {
						t.Errorf("%s, no fault: %v", c.name, err)
					}
					break
				}
				if !errors.Is(err, errFault) {
					t.Errorf("%s, %s failing after %d calls: %v", c.name, m, pass, err)
				}
				if diff := cmp.Diff(before, w.index(w.c)); diff != "" {
					t.Errorf(
						"%s, %s failing after %d calls changed the index:\n%s",
						c.name,
						m,
						pass,
						diff,
					)
				}
			}
		}
	}
}

// A version whose stored Metadata does not decode fails the write that
// would rewrite it, rather than replacing it with a guess; a reindex
// rebuilds it.
func TestUnitCorruptMetadata(t *testing.T) {
	w := newWorld(t)
	w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	w.deploy("b/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: b"))
	p, err := w.st.Packages().Find(w.ctx, w.c.Key, "web")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := w.st.Packages().Version(w.ctx, p.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	pv.Metadata = []byte("{")
	if _, err := w.st.Packages().PutVersion(w.ctx, pv); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deployTo(w.c, "c/web-1.0.0.tgz", chartTgz("web", "1.0.0")); err == nil {
		t.Fatal("deployed beside corrupt metadata")
	}
	if _, err := w.deployTo(w.c, "a/web-1.0.0.tgz", []byte("junk")); err == nil {
		t.Fatal("overwrote beside corrupt metadata")
	}
	if _, err := w.items.Delete(
		w.ctx,
		item.Target{Repo: w.c, Path: "b/web-1.0.0.tgz"},
		"t",
	); err == nil {
		t.Fatal("deleted beside corrupt metadata")
	}
	if err := w.items.ReindexPath(w.ctx, w.c, "a"); err == nil {
		t.Fatal("partly reindexed beside corrupt metadata")
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	w.described(w.c, "web", "1.0.0", "b/web-1.0.0.tgz", "b", "a/web-1.0.0.tgz", "b/web-1.0.0.tgz")
}
