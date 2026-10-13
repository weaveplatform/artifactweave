package item

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// faulty fails the store operation named fail with errBoom.
type faulty struct {
	*inmem.Store
	fail string
}

func (f faulty) Items() store.ItemStore  { return faultyItems{f.Store.Items(), f.fail} }
func (f faulty) Blobs() store.BlobIndex  { return faultyBlobs{f.Store.Blobs(), f.fail} }
func (f faulty) Trash() store.TrashStore { return faultyTrash{f.Store.Trash(), f.fail} }
func (f faulty) Packages() store.PackageStore {
	return faultyPackages{f.Store.Packages(), f.fail}
}

func (f faulty) Tx(ctx context.Context, fn func(context.Context) error) error {
	return f.Store.Tx(ctx, fn)
}

type faultyItems struct {
	store.ItemStore
	fail string
}

func (f faultyItems) Get(ctx context.Context, k string, c bool, p string) (types.Item, error) {
	if f.fail == "get" || (f.fail == "get:"+p) {
		return types.Item{}, errBoom
	}
	return f.ItemStore.Get(ctx, k, c, p)
}

func (f faultyItems) Put(ctx context.Context, it types.Item) (types.Item, error) {
	if f.fail == "put" || (f.fail == "putFolder" && it.Folder) || f.fail == "put:"+it.Name {
		return it, errBoom
	}
	return f.ItemStore.Put(ctx, it)
}

func (f faultyItems) PatchProperties(
	ctx context.Context,
	id int64,
	set types.Properties,
	rm []string,
) error {
	if f.fail == "patch" {
		return errBoom
	}
	return f.ItemStore.PatchProperties(ctx, id, set, rm)
}

func (f faultyItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if f.fail == "properties" {
		return nil, errBoom
	}
	return f.ItemStore.Properties(ctx, id)
}

func (f faultyItems) Descendants(
	ctx context.Context, k string, c bool, p string, pg store.Page,
) ([]types.Item, string, error) {
	if f.fail == "descendants" {
		return nil, "", errBoom
	}
	return f.ItemStore.Descendants(ctx, k, c, p, pg)
}

func (f faultyItems) Delete(
	ctx context.Context,
	k string,
	c bool,
	p string,
) (store.DeleteResult, error) {
	if f.fail == "delete" {
		return store.DeleteResult{}, errBoom
	}
	return f.ItemStore.Delete(ctx, k, c, p)
}

func (f faultyItems) Stats(ctx context.Context, id int64) (types.ItemStats, error) {
	if f.fail == "stats" {
		return types.ItemStats{}, errBoom
	}
	return f.ItemStore.Stats(ctx, id)
}

func (f faultyItems) PutStats(ctx context.Context, id int64, st types.ItemStats) error {
	if f.fail == "putStats" {
		return errBoom
	}
	return f.ItemStore.PutStats(ctx, id, st)
}

func (f faultyItems) Children(
	ctx context.Context,
	k string,
	c bool,
	folder string,
	p store.Page,
) ([]types.Item, string, error) {
	if f.fail == "children" {
		return nil, "", errBoom
	}
	return f.ItemStore.Children(ctx, k, c, folder, p)
}

type faultyBlobs struct {
	store.BlobIndex
	fail string
}

func (f faultyBlobs) Ref(ctx context.Context, b types.Blob, n int64) (types.Blob, error) {
	if f.fail == "ref" {
		return b, errBoom
	}
	return f.BlobIndex.Ref(ctx, b, n)
}

func (f faultyBlobs) Unref(ctx context.Context, sha string, n int64) (types.Blob, error) {
	if f.fail == "unref" {
		return types.Blob{}, errBoom
	}
	return f.BlobIndex.Unref(ctx, sha, n)
}

func (f faultyBlobs) Find(ctx context.Context, c types.Checksums) (types.Blob, error) {
	if f.fail == "find" {
		return types.Blob{}, errBoom
	}
	return f.BlobIndex.Find(ctx, c)
}

type faultyTrash struct {
	store.TrashStore
	fail string
}

func (f faultyTrash) Add(ctx context.Context, e types.TrashEntry) (types.TrashEntry, error) {
	if f.fail == "trash" {
		return e, errBoom
	}
	return f.TrashStore.Add(ctx, e)
}

type faultyPackages struct {
	store.PackageStore
	fail string
}

func (f faultyPackages) VersionOf(ctx context.Context, id int64) (types.PackageVersion, error) {
	if f.fail == "versionOf" {
		return types.PackageVersion{}, errBoom
	}
	return f.PackageStore.VersionOf(ctx, id)
}

// faultyBlobStore fails the blob operation named fail.
type faultyBlobStore struct {
	blob.Store
	fail string
}

func (f faultyBlobStore) BeginUpload(
	ctx context.Context,
	o ...blob.UploadOption,
) (blob.Upload, error) {
	if f.fail == "begin" {
		return nil, errBoom
	}
	u, err := f.Store.BeginUpload(ctx, o...)
	return faultyUpload{u, f.fail}, err
}

func (f faultyBlobStore) Exists(ctx context.Context, sha string) (bool, error) {
	if f.fail == "exists" {
		return false, errBoom
	}
	return f.Store.Exists(ctx, sha)
}

type faultyUpload struct {
	blob.Upload
	fail string
}

func (f faultyUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if f.fail == "commit" {
		return blob.Info{}, errBoom
	}
	return f.Upload.Commit(ctx, want)
}

func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	c := sums("seed")
	for _, tc := range []struct {
		fail string
		run  func(*world) error
	}{
		{"begin", func(w *world) error { _, err := w.deploy("n", "x", types.Checksums{}); return err }},
		{"commit", func(w *world) error { _, err := w.deploy("n", "x", types.Checksums{}); return err }},
		{"get:n", func(w *world) error { _, err := w.deploy("n", "x", types.Checksums{}); return err }},
		{"get:a", func(w *world) error { _, err := w.deploy("a/n", "x", types.Checksums{}); return err }},
		{"putFolder", func(w *world) error { _, err := w.deploy("a/n", "x", types.Checksums{}); return err }},
		{"put", func(w *world) error { _, err := w.deploy("n", "x", types.Checksums{}); return err }},
		{"ref", func(w *world) error { _, err := w.deploy("n", "x", types.Checksums{}); return err }},
		{"unref", func(w *world) error { _, err := w.deploy("seed", "x", types.Checksums{}); return err }},
		{"patch", func(w *world) error {
			_, err := w.svc.Deploy(ctx, Deploy{
				Target: Target{Repo: w.loc, Path: "n"}, Body: strings.NewReader("x"),
				Properties: types.Properties{"k": {"v"}},
			})
			return err
		}},
		{"patch", func(w *world) error {
			_, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "f"}, types.Properties{"k": {"v"}}, "")
			return err
		}},
		{"get", func(w *world) error {
			_, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "f"}, nil, "")
			return err
		}},
		{"find", func(w *world) error {
			_, err := w.svc.DeployByChecksum(ctx, Deploy{Target: Target{Repo: w.loc, Path: "n"}, Declared: c})
			return err
		}},
		{"exists", func(w *world) error {
			_, err := w.svc.DeployByChecksum(ctx, Deploy{Target: Target{Repo: w.loc, Path: "n"}, Declared: c})
			return err
		}},
		{"put", func(w *world) error {
			_, err := w.svc.RecordChecksum(ctx, Target{Repo: w.loc, Path: "seed"}, SHA1, c.SHA1)
			return err
		}},
		{"descendants", func(w *world) error { _, err := w.svc.Delete(ctx, Target{Repo: w.loc}, ""); return err }},
		{"get", func(w *world) error { _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "seed"}, ""); return err }},
		{"properties", func(w *world) error {
			_, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "seed"}, "")
			return err
		}},
		{"trash", func(w *world) error { _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "seed"}, ""); return err }},
		{"versionOf", func(w *world) error {
			_, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "seed"}, "")
			return err
		}},
		{"delete", func(w *world) error { _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "seed"}, ""); return err }},
		{"unref", func(w *world) error {
			_, err := w.svc.Delete(ctx, Target{Repo: w.loc, Cached: true, Path: "seed"}, "")
			return err
		}},
	} {
		w := newWorld(t)
		for _, cached := range []bool{false, true} {
			if _, err := w.svc.Deploy(ctx, Deploy{
				Target: Target{
					Repo:   w.loc,
					Cached: cached,
					Path:   "seed",
				},
				Body: strings.NewReader("seed"),
			}); err != nil {
				t.Fatal(err)
			}
		}
		w.svc.Store = faulty{w.db, tc.fail}
		w.svc.Blobs = faultyBlobStore{w.svc.Blobs, tc.fail}
		if err := tc.run(w); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", tc.fail, err)
		}
	}
}
