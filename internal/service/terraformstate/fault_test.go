package terraformstate_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/internal/testfault"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("injected fault")

// fault fails one method: it lets pass calls through, then fails the next.
// Unarmed, it counts the calls of each method instead.
type fault struct {
	mu     sync.Mutex
	method string
	pass   int
	hit    bool
	calls  map[string]int
}

func (f *fault) fail(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

// faultyStore fails the call its fault names.
type faultyStore struct {
	*inmem.Store
	f *fault
}

func (s faultyStore) Items() store.ItemStore { return faultyItems{s.Store.Items(), s.f} }

func (s faultyStore) Packages() store.PackageStore { return faultyPackages{s.Store.Packages(), s.f} }
func (s faultyStore) Repos() store.RepoStore       { return faultyRepos{s.Store.Repos(), s.f} }
func (s faultyStore) Blobs() store.BlobIndex       { return faultyIndex{s.Store.Blobs(), s.f} }

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
func (i faultyItems) Put(ctx context.Context, it types.Item) (types.Item, error) {
	if err := i.f.fail("items.Put"); err != nil {
		return types.Item{}, err
	}
	return i.ItemStore.Put(ctx, it)
}

//nolint:wrapcheck // test double
func (i faultyItems) Children(
	ctx context.Context, k string, c bool, folder string, p store.Page,
) ([]types.Item, string, error) {
	if err := i.f.fail("items.Children"); err != nil {
		return nil, "", err
	}
	return i.ItemStore.Children(ctx, k, c, folder, p)
}

//nolint:wrapcheck // test double
func (i faultyItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if err := i.f.fail("items.Search"); err != nil {
		return nil, err
	}
	return i.ItemStore.Search(ctx, q, o)
}

//nolint:wrapcheck // test double
func (i faultyItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if err := i.f.fail("items.Properties"); err != nil {
		return nil, err
	}
	return i.ItemStore.Properties(ctx, id)
}

//nolint:wrapcheck // test double
func (i faultyItems) PatchProperties(
	ctx context.Context, id int64, set types.Properties, rm []string, at time.Time,
) error {
	if err := i.f.fail("items.PatchProperties"); err != nil {
		return err
	}
	return i.ItemStore.PatchProperties(ctx, id, set, rm, at)
}

//nolint:wrapcheck // test double
func (i faultyItems) Delete(
	ctx context.Context, k string, c bool, p string,
) (store.DeleteResult, error) {
	if err := i.f.fail("items.Delete"); err != nil {
		return store.DeleteResult{}, err
	}
	return i.ItemStore.Delete(ctx, k, c, p)
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
	ctx context.Context, id int64, v string,
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

type faultyRepos struct {
	store.RepoStore
	f *fault
}

//nolint:wrapcheck // test double
func (r faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if err := r.f.fail("repos.Get"); err != nil {
		return repo.Config{}, err
	}
	return r.RepoStore.Get(ctx, key)
}

//nolint:wrapcheck // test double
func (r faultyRepos) List(ctx context.Context, f store.RepoFilter) ([]repo.Config, error) {
	if err := r.f.fail("repos.List"); err != nil {
		return nil, err
	}
	return r.RepoStore.List(ctx, f)
}

type faultyIndex struct {
	store.BlobIndex
	f *fault
}

//nolint:wrapcheck // test double
func (b faultyIndex) Ref(ctx context.Context, bl types.Blob, n int64) (types.Blob, error) {
	if err := b.f.fail("blobs.Ref"); err != nil {
		return types.Blob{}, err
	}
	return b.BlobIndex.Ref(ctx, bl, n)
}

//nolint:wrapcheck // test double
func (b faultyIndex) Unref(ctx context.Context, sha string, n int64) (types.Blob, error) {
	if err := b.f.fail("blobs.Unref"); err != nil {
		return types.Blob{}, err
	}
	return b.BlobIndex.Unref(ctx, sha, n)
}

// faultyBlobs fails the blob store's calls its fault names.
type faultyBlobs struct {
	blob.Store
	f *fault
}

//nolint:wrapcheck // test double
func (b faultyBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if err := b.f.fail("content.Open"); err != nil {
		return nil, blob.Info{}, err
	}
	return b.Store.Open(ctx, sha)
}

//nolint:wrapcheck // test double
func (b faultyBlobs) BeginUpload(ctx context.Context, o ...blob.UploadOption) (blob.Upload, error) {
	if err := b.f.fail("content.BeginUpload"); err != nil {
		return nil, err
	}
	return b.Store.BeginUpload(ctx, o...)
}

var faultMethods = []string{
	"items.Get", "items.Put", "items.Children", "items.Search", "items.Properties",
	"items.PatchProperties", "items.Delete",
	"packages.Find", "packages.Ensure", "packages.Version", "packages.Versions",
	"packages.DeleteVersion", "packages.Delete", "packages.VersionOf",
	"repos.Get", "repos.List", "blobs.Ref", "blobs.Unref", "content.Open", "content.BeginUpload",
}

// faultOps are operations over a world holding tf-local's workspace app,
// with two states written while bob holds its lock, and an empty one,
// idle.
var faultOps = map[string]func(w *world) error{
	"Workspaces": func(w *world) error { _, err := w.svc.Workspaces(w.ctx, w.c); return err },
	"CreateWorkspace": func(w *world) error {
		_, err := w.svc.CreateWorkspace(w.ctx, w.c, "new", "", "bob")
		return err
	},
	"DeleteWorkspace": func(w *world) error { return w.svc.DeleteWorkspace(w.ctx, w.c, "app", "bob") },
	"Lock":            func(w *world) error { _, err := w.svc.Lock(w.ctx, w.c, "idle", "bob"); return err },
	"Unlock":          func(w *world) error { _, err := w.svc.Unlock(w.ctx, w.c, "app", "bob"); return err },
	"ForceUnlock":     func(w *world) error { _, err := w.svc.ForceUnlock(w.ctx, w.c, "app"); return err },
	"WriteState":      func(w *world) error { _, err := w.write(w.c, "app", 3, "bob"); return err },
	"NewState":        func(w *world) error { _, err := w.svc.NewState(w.ctx, w.c, "app", "bob"); return err },
	"States":          func(w *world) error { _, err := w.svc.States(w.ctx, w.c, "app"); return err },
	"ReadState": func(w *world) error {
		st, err := w.svc.CurrentState(w.ctx, w.c, "app")
		if err == nil {
			_, _, err = w.svc.ReadState(w.ctx, w.c, "app", st.Ms)
		}
		return err
	},
	"ResolveState": func(w *world) error {
		states, err := w.svc.States(w.ctx, w.c, "app")
		if err == nil {
			_, _, err = w.svc.ResolveState(w.ctx, states[1].ID())
		}
		return err
	},
	"Reseal": func(w *world) error {
		w.useKeys(keyring(w.t, masterKey(1, 'a'), masterKey(2, 'b')))
		_, err := w.svc.Reseal(w.ctx)
		return err
	},
	"Reindex": func(w *world) error {
		pkgs, _, err := w.st.Packages().List(w.ctx, tfKey, store.Page{Limit: 10})
		for _, p := range pkgs {
			err = errors.Join(err, w.st.Packages().Delete(w.ctx, p.ID))
		}
		return errors.Join(err, w.items.Reindex(w.ctx, w.c))
	},
	"delete the current state": func(w *world) error {
		_, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: tfbe.LatestPath("app")}, "x")
		return err
	},
	"restore the workspace": func(w *world) error {
		_, err := w.trash.Restore(w.ctx, tfKey, tfbe.Folder("gone"), trash.Destination{Repo: w.c},
			"admin", nil)
		return err
	},
}

// Every store or content failure an operation meets fails it, whichever
// call fails and however late.
func TestUnitFaults(t *testing.T) {
	for name, op := range faultOps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// A run without faults counts the calls, which bounds the sweep.
			counter := &fault{}
			w := faultyWorld(t, counter)
			counter.calls = map[string]int{}
			if err := op(w); err != nil {
				t.Fatal(err)
			}
			for _, method := range faultMethods {
				for pass := range counter.calls[method] {
					f := &fault{}
					w := faultyWorld(t, f)
					f.method, f.pass = method, pass
					if err := op(w); err == nil {
						t.Errorf("%s failing after %d calls: the operation succeeded", method, pass)
					}
				}
			}
		})
	}
}

// faultyWorld is the world the fault operations act on, over a store and
// content that fail as f says once it is armed.
func faultyWorld(t *testing.T, f *fault) *world {
	t.Helper()
	w := newWorld(t, faultyStore{inmem.New(), f})
	w.blobs = faultyBlobs{w.blobs, f}
	w.useKeys(w.keys)
	w.workspace(w.c, "app")
	w.workspace(w.c, "idle")
	w.workspace(w.c, "gone")
	if _, err := w.svc.Lock(w.ctx, w.c, "app", "bob"); err != nil {
		t.Fatal(err)
	}
	for serial := int64(1); serial <= 2; serial++ {
		if _, err := w.write(w.c, "app", serial, "bob"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.write(w.c, "gone", serial, "bob"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.svc.DeleteWorkspace(w.ctx, w.c, "gone", "bob"); err != nil {
		t.Fatal(err)
	}
	return w
}

// A state whose upload fails, or whose client goes away while sending it,
// is not stored, and the current state stays.
func TestUnitUploadFaults(t *testing.T) {
	body := state(2, "lin-app")
	for name, tc := range map[string]struct {
		blobs func(blob.Store) blob.Store
		body  io.Reader
	}{
		"append": {
			blobs: func(b blob.Store) blob.Store { return testfault.Blobs{Store: b, Append: errFault} },
			body:  bytesReader(body),
		},
		"commit": {
			blobs: func(b blob.Store) blob.Store { return testfault.Blobs{Store: b, Commit: errFault} },
			body:  bytesReader(body),
		},
		"disconnect": {
			blobs: func(b blob.Store) blob.Store { return b },
			body:  testfault.Disconnect(bytesReader(body), 10),
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, memStore())
			w.workspace(w.c, "app")
			first := w.mustWrite(w.c, "app", 1)
			w.blobs = tc.blobs(w.blobs)
			w.useKeys(w.keys)
			w.tick()
			if _, err := w.svc.WriteState(w.ctx, w.c, "app", terraformstate.StateWrite{
				Body: tc.body, MD5: tfbe.MD5(body), Serial: 2, By: "alice",
			}); err == nil {
				t.Fatal("the write succeeded")
			}
			cur, err := w.svc.CurrentState(w.ctx, w.c, "app")
			if err != nil || cur.Ms != first.Ms {
				t.Fatalf("the current state = %+v, %v", cur, err)
			}
		})
	}
}
