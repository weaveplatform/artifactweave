package gc_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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

type conflictBlobs struct{ store.BlobIndex }

func (conflictBlobs) Delete(context.Context, string) error { return store.ErrConflict }

type conflicting struct{ failing }

func (c conflicting) Blobs() store.BlobIndex { return conflictBlobs{c.Store.Blobs()} }

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
	collect := func(t *testing.T, st store.Store, blobs blob.Store) (gc.Report, error) {
		t.Helper()
		w := newWorld(t)
		w.trashOff(t)
		w.deploy(t, "f.txt", "content")
		w.remove(t, "f.txt")
		w.advance(2 * gc.DefaultGrace)
		if st == nil {
			st = w.db
		}
		if r, ok := st.(racing); ok {
			r.Store = w.db
			st = r
		}
		if c, ok := st.(conflicting); ok {
			c.Store = w.db
			st = c
		}
		c, err := gc.New(
			st,
			failBlobStore{Store: w.blobs, del: delErr(blobs)},
			gc.Options{Now: w.now},
		)
		if err != nil {
			t.Fatal(err)
		}
		return c.Run(ctx)
	}
	if _, err := collect(t, racing{get: func(n int) (types.Blob, error, bool) {
		return types.Blob{}, errBoom, n == 1
	}, n: &atomic.Int64{}}, nil); !errors.Is(err, errBoom) {
		t.Fatalf("index read failure: %v", err)
	}
	if rep, err := collect(t, racing{get: func(n int) (types.Blob, error, bool) {
		return types.Blob{}, store.ErrNotFound, n == 1
	}, n: &atomic.Int64{}}, nil); err != nil || rep.BlobsDeleted != 0 {
		t.Fatalf("a row deleted concurrently is skipped: %+v, %v", rep, err)
	}
	if rep, err := collect(t, conflicting{}, nil); err != nil || rep.Kept != 1 {
		t.Fatalf("a delete refused while referenced keeps the blob: %+v, %v", rep, err)
	}
	if _, err := collect(t, nil, failBlobStore{del: errBoom}); !errors.Is(err, errBoom) {
		t.Fatalf("content delete failure: %v", err)
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
	deleteFails, err := gc.New(
		w.db,
		failBlobStore{Store: w.blobs, del: errBoom},
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
	// Indexed between the walk and the delete: kept.
	c := w.collector(t, racing{Store: w.db, get: func(n int) (types.Blob, error, bool) {
		return types.Blob{}, nil, n == 3
	}, n: &atomic.Int64{}})
	run(t, c)
	w.advance(2 * gc.DefaultGrace)
	if rep := run(t, c); rep.OrphansDeleted != 0 {
		t.Fatalf("an orphan indexed after the walk is kept: %+v", rep)
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
