package gc_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// failBlobStore fails the blob store's Delete or Walk.
type failBlobStore struct {
	blob.Store
	del, walk error
}

func (f failBlobStore) Delete(ctx context.Context, sha string) error {
	if f.del != nil {
		return f.del
	}
	return f.Store.Delete(ctx, sha) //nolint:wrapcheck // test double
}

func (f failBlobStore) Walk(ctx context.Context, fn func(blob.Info) error) error {
	if f.walk != nil {
		return f.walk
	}
	return f.Store.Walk(ctx, fn) //nolint:wrapcheck // test double
}

type failDeleteUploads struct{ store.UploadStore }

func (failDeleteUploads) Delete(context.Context, string) error { return errBoom }

type uploadsFailing struct{ failing }

func (u uploadsFailing) Uploads() store.UploadStore {
	return failDeleteUploads{u.Store.Uploads()}
}

// faultBlobs fails chosen blob index calls.
type faultBlobs struct {
	store.BlobIndex
	mark, finish, deleting, get, prune error
}

func (f faultBlobs) MarkDeleting(
	ctx context.Context,
	b types.Blob,
	at time.Time,
) (types.Blob, error) {
	if f.mark != nil {
		return types.Blob{}, f.mark
	}
	return f.BlobIndex.MarkDeleting(ctx, b, at) //nolint:wrapcheck // test double
}

func (f faultBlobs) FinishDelete(ctx context.Context, sha string, at time.Time) error {
	if f.finish != nil {
		return f.finish
	}
	return f.BlobIndex.FinishDelete(ctx, sha, at) //nolint:wrapcheck // test double
}

func (f faultBlobs) Deleting(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	if f.deleting != nil {
		return nil, "", f.deleting
	}
	return f.BlobIndex.Deleting(ctx, p) //nolint:wrapcheck // test double
}

func (f faultBlobs) Get(ctx context.Context, sha string) (types.Blob, error) {
	if f.get != nil {
		return types.Blob{}, f.get
	}
	return f.BlobIndex.Get(ctx, sha) //nolint:wrapcheck // test double
}

func (f faultBlobs) Prune(ctx context.Context, before time.Time) (int64, error) {
	if f.prune != nil {
		return 0, f.prune
	}
	return f.BlobIndex.Prune(ctx, before) //nolint:wrapcheck // test double
}

// faulty is a store whose blob index fails as faults says.
type faulty struct {
	failing
	faults faultBlobs
}

func (f faulty) Blobs() store.BlobIndex {
	b := f.faults
	b.BlobIndex = f.Store.Blobs()
	return b
}

func TestDefaults(t *testing.T) {
	w := newWorld(t)
	c, err := gc.New(w.db, w.blobs, gc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManyUnreferenced(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	const n = 501 // more than one page
	for i := range n {
		sha := fmt.Sprintf("%064x", i)
		if _, err := w.db.Blobs().Ref(ctx, types.Blob{
			Checksums: types.Checksums{SHA256: sha}, Size: 1, Created: w.now(),
		}, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Blobs().Unref(ctx, sha, 1); err != nil {
			t.Fatal(err)
		}
	}
	w.advance(2 * gc.DefaultGrace)
	if rep := run(t, w.collector(t, w.db)); rep.BlobsDeleted != n {
		t.Fatalf("deleted %d of %d", rep.BlobsDeleted, n)
	}
}

func TestDeleteFailures(t *testing.T) {
	ctx := context.Background()
	// prepare leaves one unreferenced blob past the grace period.
	prepare := func(t *testing.T) (*world, string) {
		t.Helper()
		w := newWorld(t)
		w.trashOff(t)
		sha := w.deploy(t, "f.txt", "content").Actual.SHA256
		w.remove(t, "f.txt")
		w.advance(2 * gc.DefaultGrace)
		return w, sha
	}
	collect := func(t *testing.T, w *world, faults faultBlobs, blobs blob.Store) (gc.Report, error) {
		t.Helper()
		c, err := gc.New(faulty{failing{Store: w.db}, faults}, blobs, gc.Options{Now: w.now})
		if err != nil {
			t.Fatal(err)
		}
		return c.Run(ctx)
	}
	w, sha := prepare(t)
	if rep, err := collect(t, w, faultBlobs{mark: store.ErrConflict}, w.blobs); err != nil ||
		rep.Kept != 1 || !w.stored(t, sha) {
		t.Fatalf("a blob referenced again is kept: %+v, %v", rep, err)
	}
	if _, err := collect(t, w, faultBlobs{mark: errBoom}, w.blobs); !errors.Is(err, errBoom) {
		t.Fatalf("mark failure: %v", err)
	}
	if _, err := collect(t, w, faultBlobs{finish: errBoom}, w.blobs); !errors.Is(err, errBoom) {
		t.Fatalf("finish failure: %v", err)
	}

	// A content delete that fails leaves the row deleting: deploys of the
	// content are refused until the next pass finishes the removal.
	w, sha = prepare(t)
	if _, err := collect(
		t,
		w,
		faultBlobs{},
		failBlobStore{Store: w.blobs, del: errBoom},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("content delete failure: %v", err)
	}
	if b, err := w.db.Blobs().Get(ctx, sha); err != nil || b.State != types.BlobDeleting {
		t.Fatalf("row after a failed content delete: %+v, %v", b, err)
	}
	if _, err := collect(t, w, faultBlobs{deleting: errBoom}, w.blobs); !errors.Is(err, errBoom) {
		t.Fatalf("listing deleting rows: %v", err)
	}
	rep, err := collect(t, w, faultBlobs{}, w.blobs)
	if b, gerr := w.db.Blobs().
		Get(ctx, sha); err != nil || rep.BlobsDeleted != 1 || w.stored(t, sha) ||
		gerr != nil ||
		b.State != types.BlobDeleted {
		t.Fatalf("the next pass finishes the removal: %+v, %v; row %+v, %v", rep, err, b, gerr)
	}
	if _, err := collect(t, w, faultBlobs{prune: errBoom}, w.blobs); !errors.Is(err, errBoom) {
		t.Fatalf("prune failure: %v", err)
	}
}

// delErr extracts the delete error a test asked for, if any.
func delErr(b blob.Store) error {
	if f, ok := b.(failBlobStore); ok {
		return f.del
	}
	return nil
}

func TestOrphanFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	blobtest.Put(ctx, t, w.blobs, []byte("orphan"))
	walkFails, err := gc.New(
		w.db,
		failBlobStore{Store: w.blobs, walk: errBoom},
		gc.Options{Now: w.now},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := walkFails.Run(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("walk failure: %v", err)
	}
	// An index that cannot be read must not make content look orphaned.
	readFails, err := gc.New(faulty{failing{Store: w.db}, faultBlobs{get: errBoom}}, w.blobs,
		gc.Options{Now: w.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readFails.Run(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("index read failure in the walk: %v", err)
	}
	deleteFails, err := gc.New(
		w.db,
		failBlobStore{Store: w.blobs, del: delErr(failBlobStore{del: errBoom})},
		gc.Options{Now: w.now},
	)
	if err != nil {
		t.Fatal(err)
	}
	run(t, deleteFails)
	w.advance(2 * gc.DefaultGrace)
	if _, err := deleteFails.Run(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("orphan delete failure: %v", err)
	}
}

func TestReapFailure(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	old := w.now().Add(-48 * time.Hour)
	if err := w.db.Uploads().
		Put(ctx, store.Upload{ID: "x", Started: old, Updated: old}); err != nil {
		t.Fatal(err)
	}
	_, err := w.collector(t, uploadsFailing{failing{Store: w.db}}).Run(ctx)
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "drop upload") {
		t.Fatalf("drop failure: %v", err)
	}
}
