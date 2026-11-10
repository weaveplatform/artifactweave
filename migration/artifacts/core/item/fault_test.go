package item

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
)

var errBoom = errors.New("boom")

// faulty fails the store operation named fail with errBoom.
type faulty struct {
	*inmem.Store
	fail string
}

func (f faulty) Items() port.ItemStore  { return faultyItems{f.Store.Items(), f.fail} }
func (f faulty) Blobs() port.BlobIndex  { return faultyBlobs{f.Store.Blobs(), f.fail} }
func (f faulty) Trash() port.TrashStore { return faultyTrash{f.Store.Trash(), f.fail} }
func (f faulty) Packages() port.PackageStore {
	return faultyPackages{f.Store.Packages(), f.fail}
}

func (f faulty) Tx(ctx context.Context, fn func(context.Context) error) error {
	return f.Store.Tx(ctx, fn)
}

type faultyItems struct {
	port.ItemStore
	fail string
}

func (f faultyItems) Get(ctx context.Context, k string, c bool, p string) (model.Item, error) {
	if f.fail == "get" || (f.fail == "get:"+p) {
		return model.Item{}, errBoom
	}
	return f.ItemStore.Get(ctx, k, c, p)
}

func (f faultyItems) Put(ctx context.Context, it model.Item) (model.Item, error) {
	if f.fail == "put" || (f.fail == "putFolder" && it.Folder) || f.fail == "put:"+it.Name {
		return it, errBoom
	}
	return f.ItemStore.Put(ctx, it)
}

func (f faultyItems) PatchProperties(
	ctx context.Context,
	id int64,
	set model.Properties,
	rm []string,
	at time.Time,
) error {
	if f.fail == "patch" {
		return errBoom
	}
	return f.ItemStore.PatchProperties(ctx, id, set, rm, at)
}

func (f faultyItems) SetProperties(
	ctx context.Context,
	id int64,
	props model.Properties,
	at time.Time,
) error {
	if f.fail == "set" {
		return errBoom
	}
	return f.ItemStore.SetProperties(ctx, id, props, at)
}

func (f faultyItems) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if f.fail == "properties" {
		return nil, errBoom
	}
	return f.ItemStore.Properties(ctx, id)
}

func (f faultyItems) Descendants(
	ctx context.Context, k string, c bool, p string, pg kernel.Page,
) ([]model.Item, string, error) {
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
) (port.DeleteResult, error) {
	if f.fail == "delete" || f.fail == "delete:"+p {
		return port.DeleteResult{}, errBoom
	}
	return f.ItemStore.Delete(ctx, k, c, p)
}

func (f faultyItems) Stats(ctx context.Context, id int64) (model.ItemStats, error) {
	if f.fail == "stats" {
		return model.ItemStats{}, errBoom
	}
	return f.ItemStore.Stats(ctx, id)
}

func (f faultyItems) PutStats(ctx context.Context, id int64, st model.ItemStats) error {
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
	p kernel.Page,
) ([]model.Item, string, error) {
	if f.fail == "children" {
		return nil, "", errBoom
	}
	return f.ItemStore.Children(ctx, k, c, folder, p)
}

type faultyBlobs struct {
	port.BlobIndex
	fail string
}

func (f faultyBlobs) Ref(ctx context.Context, b blob.Blob, n int64) (blob.Blob, error) {
	if f.fail == "ref" {
		return b, errBoom
	}
	return f.BlobIndex.Ref(ctx, b, n)
}

func (f faultyBlobs) Unref(ctx context.Context, sha string, n int64) (blob.Blob, error) {
	if f.fail == "unref" {
		return blob.Blob{}, errBoom
	}
	return f.BlobIndex.Unref(ctx, sha, n)
}

func (f faultyBlobs) Find(ctx context.Context, c kernel.Checksums) (blob.Blob, error) {
	if f.fail == "find" {
		return blob.Blob{}, errBoom
	}
	return f.BlobIndex.Find(ctx, c)
}

type faultyTrash struct {
	port.TrashStore
	fail string
}

func (f faultyTrash) Add(ctx context.Context, e model.TrashEntry) (model.TrashEntry, error) {
	if f.fail == "trash" {
		return e, errBoom
	}
	return f.TrashStore.Add(ctx, e)
}

type faultyPackages struct {
	port.PackageStore
	fail string
}

func (f faultyPackages) VersionOf(ctx context.Context, id int64) (model.PackageVersion, error) {
	if f.fail == "versionOf" {
		return model.PackageVersion{}, errBoom
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

func TestUnitStoreFailures(t *testing.T) {
	ctx := context.Background()
	c := sums("seed")
	for _, tc := range []struct {
		fail string
		run  func(*world) error
	}{
		{"begin", func(w *world) error { _, err := w.deploy("n", "x", kernel.Checksums{}); return err }},
		{"commit", func(w *world) error { _, err := w.deploy("n", "x", kernel.Checksums{}); return err }},
		{"get:n", func(w *world) error { _, err := w.deploy("n", "x", kernel.Checksums{}); return err }},
		{"get:a", func(w *world) error { _, err := w.deploy("a/n", "x", kernel.Checksums{}); return err }},
		{"putFolder", func(w *world) error { _, err := w.deploy("a/n", "x", kernel.Checksums{}); return err }},
		{"put", func(w *world) error { _, err := w.deploy("n", "x", kernel.Checksums{}); return err }},
		{"ref", func(w *world) error { _, err := w.deploy("n", "x", kernel.Checksums{}); return err }},
		{"unref", func(w *world) error { _, err := w.deploy("seed", "x", kernel.Checksums{}); return err }},
		{"set", func(w *world) error {
			_, err := w.svc.Deploy(ctx, Deploy{
				Target: Target{Repo: w.loc, Path: "n"}, Body: strings.NewReader("x"),
				Properties: model.Properties{"k": {"v"}},
			})
			return err
		}},
		{"stats", func(w *world) error { _, err := w.deploy("seed", "x", kernel.Checksums{}); return err }},
		{"putStats", func(w *world) error {
			seed, ok := w.get(w.loc, "seed")
			if !ok {
				return errors.New("no seed")
			}
			if err := w.db.Items().RecordDownload(ctx, seed.ID, port.Download{At: at}); err != nil {
				return err //nolint:wrapcheck // test setup
			}
			_, err := w.deploy("seed", "x", kernel.Checksums{})
			return err
		}},
		{"patch", func(w *world) error {
			_, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "f"}, model.Properties{"k": {"v"}}, "")
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
