package terraformbackend_test

import (
	"context"
	"errors"
	"testing"
	"time"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("injected fault")

// fault fails one method once armed: it lets pass calls through, then
// fails the next. Unarmed, it counts the calls of each method.
type fault struct {
	method string
	pass   int
	hit    bool
	calls  map[string]int
}

func (f *fault) fail(method string) error {
	if f.calls != nil {
		f.calls[method]++
	}
	if f.method != method || f.hit {
		return nil
	}
	if f.pass > 0 {
		f.pass--
		return nil
	}
	f.hit = true
	return errFault
}

type faultyStore struct {
	store.Store
	f *fault
}

func (s faultyStore) Items() store.ItemStore { return faultyItems{s.Store.Items(), s.f} }

func (s faultyStore) Packages() store.PackageStore { return faultyPackages{s.Store.Packages(), s.f} }

type faultyItems struct {
	store.ItemStore
	f *fault
}

//nolint:wrapcheck // test double
func (i faultyItems) Get(ctx context.Context, k string, c bool, p string) (types.Item, error) {
	if err := i.f.fail("items.Get"); err != nil {
		return types.Item{}, err
	}
	return i.ItemStore.Get(ctx, k, c, p)
}

//nolint:wrapcheck // test double
func (i faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if err := i.f.fail("items.Search"); err != nil {
		return nil, err
	}
	return i.ItemStore.Search(ctx, q, o)
}

type faultyPackages struct {
	store.PackageStore
	f *fault
}

//nolint:wrapcheck // test double
func (p faultyPackages) Find(ctx context.Context, k, name string) (types.Package, error) {
	if err := p.f.fail("packages.Find"); err != nil {
		return types.Package{}, err
	}
	return p.PackageStore.Find(ctx, k, name)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Ensure(ctx context.Context, pk types.Package) (types.Package, error) {
	if err := p.f.fail("packages.Ensure"); err != nil {
		return types.Package{}, err
	}
	return p.PackageStore.Ensure(ctx, pk)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Version(
	ctx context.Context,
	id int64,
	v string,
) (types.PackageVersion, error) {
	if err := p.f.fail("packages.Version"); err != nil {
		return types.PackageVersion{}, err
	}
	return p.PackageStore.Version(ctx, id, v)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if err := p.f.fail("packages.Versions"); err != nil {
		return nil, err
	}
	return p.PackageStore.Versions(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) DeleteVersion(ctx context.Context, id int64) error {
	if err := p.f.fail("packages.DeleteVersion"); err != nil {
		return err
	}
	return p.PackageStore.DeleteVersion(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) Delete(ctx context.Context, id int64) error {
	if err := p.f.fail("packages.Delete"); err != nil {
		return err
	}
	return p.PackageStore.Delete(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyPackages) VersionOf(ctx context.Context, id int64) (types.PackageVersion, error) {
	if err := p.f.fail("packages.VersionOf"); err != nil {
		return types.PackageVersion{}, err
	}
	return p.PackageStore.VersionOf(ctx, id)
}

var faultMethods = []string{
	"items.Get", "items.Search", "packages.Find", "packages.Ensure", "packages.Version",
	"packages.Versions", "packages.DeleteVersion", "packages.Delete", "packages.VersionOf",
}

// faultWorld holds workspace app with an earlier state, and a current one
// deleted to the trash can and written again since.
func faultWorld(t *testing.T, f *fault) *world {
	t.Helper()
	w := newWorld(t, nil)
	w.st = faultyStore{w.st, f}
	w.items.Store = w.st
	w.mustPut(w.writes, w.tf, tfbe.StatePath("app", 5), []byte(aState))
	w.mustPut(w.writes, w.tf, tfbe.LatestPath("app"), []byte(aState))
	if _, err := w.items.Delete(
		w.ctx,
		item.Target{Repo: w.tf, Path: tfbe.LatestPath("app")},
		"x",
	); err != nil {
		t.Fatal(err)
	}
	w.mustPut(w.writes, w.tf, tfbe.LatestPath("app"), []byte(`{"serial":9}`))
	return w
}

var hookOps = map[string]func(w *world) error{
	"Deployed": func(w *world) error {
		_, err := w.put(w.writes, w.tf, tfbe.StatePath("app", 6), []byte(aState))
		return err
	},
	"Deleted": func(w *world) error {
		_, err := w.items.Delete(w.ctx, item.Target{Repo: w.tf, Path: tfbe.LatestPath("app")}, "x")
		return err
	},
	"Reindex": func(w *world) error { return w.items.Reindex(w.ctx, w.tf) },
	"restore over the current state": func(w *world) error {
		w.now = w.now.Add(time.Second)
		_, err := (&trash.Service{Store: w.st, Items: w.items}).Restore(w.ctx, "tf-local",
			tfbe.LatestPath("app"), trash.Destination{Repo: w.tf}, "admin", nil)
		return err
	},
}

// Every store failure a hook meets fails the write it runs in.
func TestHookFaults(t *testing.T) {
	for name, op := range hookOps {
		t.Run(name, func(t *testing.T) {
			counter := &fault{}
			w := faultWorld(t, counter)
			counter.calls = map[string]int{}
			if err := op(w); err != nil {
				t.Fatal(err)
			}
			for _, method := range faultMethods {
				for pass := range counter.calls[method] {
					f := &fault{}
					w := faultWorld(t, f)
					f.method, f.pass = method, pass
					if err := op(w); err == nil {
						t.Errorf("%s failing after %d calls: the operation succeeded", method, pass)
					}
				}
			}
		})
	}
}

// A version the tree no longer backs, though a file still links it, goes,
// and its workspace's package with it when it was the last.
func TestUnbackedVersionGoes(t *testing.T) {
	w := newWorld(t, nil)
	it := w.mustPut(w.writes, w.tf, tfbe.StatePath("app", 6), []byte(aState))
	if err := pkgindex.Link(w.ctx, w.st, pkgindex.Version{
		RepoKey: "tf-local", PackageType: repo.TerraformBackend, Package: "ghost", Version: "5",
	}, it.ID, w.now); err != nil {
		t.Fatal(err)
	}
	gone := types.Item{RepoKey: "tf-local", Name: tfbe.StatePath("ghost", 5)}
	if err := w.h.Deleted(w.ctx, w.items.Env(), w.tf, []types.Item{gone}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Packages().
		Find(w.ctx, "tf-local", "ghost"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("the unbacked workspace's package = %v", err)
	}
}
