package gc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type world struct {
	db    *inmem.Store
	blobs *fs.Store
	items *item.Service
	libs  repo.Config
	clock atomic.Pointer[time.Time]
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{db: inmem.New(), blobs: blobs}
	w.set(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	w.items = &item.Service{Store: w.db, Blobs: blobs, Now: w.now}
	if err := w.db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	w.libs, _ = w.db.Repos().Get(ctx, "libs")
	return w
}

func (w *world) now() time.Time          { return *w.clock.Load() }
func (w *world) set(t time.Time)         { w.clock.Store(&t) }
func (w *world) advance(d time.Duration) { w.set(w.now().Add(d)) }

func (w *world) collector(t *testing.T, st store.Store) *gc.Collector {
	t.Helper()
	c, err := gc.New(st, w.blobs, gc.Options{
		Now: w.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (w *world) deploy(t *testing.T, p, body string) types.Item {
	t.Helper()
	it, err := w.items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: w.libs, Path: p}, Body: strings.NewReader(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (w *world) remove(t *testing.T, p string) {
	t.Helper()
	if _, err := w.items.Delete(
		context.Background(),
		item.Target{Repo: w.libs, Path: p},
		"alice",
	); err != nil {
		t.Fatal(err)
	}
}

func (w *world) trashOff(t *testing.T) {
	t.Helper()
	if err := w.db.Settings().Put(context.Background(), types.SettingTrashcan,
		types.TrashcanSettings{Enabled: false, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
}

func (w *world) stored(t *testing.T, sha string) bool {
	t.Helper()
	ok, err := w.blobs.Exists(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func run(t *testing.T, c *gc.Collector) gc.Report {
	t.Helper()
	rep, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestUnreferencedAfterGrace(t *testing.T) {
	w := newWorld(t)
	w.trashOff(t)
	sha := w.deploy(t, "f.txt", "content").Actual.SHA256
	w.remove(t, "f.txt")
	c := w.collector(t, w.db)
	if rep := run(t, c); rep.BlobsDeleted != 0 || !w.stored(t, sha) {
		t.Fatalf("collected within the grace period: %+v", rep)
	}
	w.advance(gc.DefaultGrace + time.Minute)
	rep := run(t, c)
	if rep.BlobsDeleted != 1 || rep.BytesFreed != int64(len("content")) || w.stored(t, sha) {
		t.Fatalf("after the grace period: %+v", rep)
	}
	if _, err := w.db.Blobs().Get(context.Background(), sha); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("index row left: %v", err)
	}
}

func TestTrashPurgedThenCollected(t *testing.T) {
	w := newWorld(t)
	sha := w.deploy(t, "f.txt", "content").Actual.SHA256
	keep := w.deploy(t, "keep.txt", "kept content").Actual.SHA256
	w.remove(t, "f.txt")
	c := w.collector(t, w.db)
	if rep := run(t, c); rep.TrashPurged != 0 || !w.stored(t, sha) {
		t.Fatalf("a trashed file within retention is kept: %+v", rep)
	}
	w.advance(15 * 24 * time.Hour)
	rep := run(t, c)
	if rep.TrashPurged != 1 || rep.BlobsDeleted != 1 || w.stored(t, sha) || !w.stored(t, keep) {
		t.Fatalf("expired trash and its blob: %+v", rep)
	}
}

func TestReapUploads(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	live, err := w.blobs.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old := w.now().Add(-48 * time.Hour)
	for _, u := range []store.Upload{
		{ID: live.ID(), Backend: "fs", Started: old, Updated: old},
		{ID: "gone", Backend: "s3:cold_blob", Started: old, Updated: old},
		{ID: "fresh", Backend: "fs", Started: w.now(), Updated: w.now()},
	} {
		if err := w.db.Uploads().Put(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	rep := run(t, w.collector(t, w.db))
	if rep.UploadsReaped != 2 {
		t.Fatalf("reaped %+v", rep)
	}
	if _, err := w.blobs.ResumeUpload(ctx, live.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("a reaped session is aborted in its blob store: %v", err)
	}
	if _, err := w.db.Uploads().Get(ctx, "fresh"); err != nil {
		t.Fatalf("a fresh session is kept: %v", err)
	}
}

func TestOrphans(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	orphan := blobtest.Put(ctx, t, w.blobs, []byte("orphan")).Digests.SHA256
	adopted := blobtest.Put(ctx, t, w.blobs, []byte("adopted")).Digests.SHA256
	vanished := blobtest.Put(ctx, t, w.blobs, []byte("vanished")).Digests.SHA256
	c := w.collector(t, w.db)
	if rep := run(t, c); rep.OrphansDeleted != 0 {
		t.Fatalf("an orphan seen for the first time is kept: %+v", rep)
	}
	// Indexed between passes, as a deploy committing late would be.
	if _, err := w.db.Blobs().Ref(ctx, types.Blob{
		Checksums: types.Checksums{SHA256: adopted}, Size: int64(len("adopted")), Created: w.now(),
	}, 1); err != nil {
		t.Fatal(err)
	}
	if err := w.blobs.Delete(ctx, vanished); err != nil {
		t.Fatal(err)
	}
	w.advance(gc.DefaultGrace / 2)
	if rep := run(t, c); rep.OrphansDeleted != 0 {
		t.Fatalf("within the grace period: %+v", rep)
	}
	w.advance(gc.DefaultGrace)
	rep := run(t, c)
	if rep.OrphansDeleted != 1 || w.stored(t, orphan) || !w.stored(t, adopted) {
		t.Fatalf("after the grace period: %+v", rep)
	}
}

func TestLeaseHeldElsewhere(t *testing.T) {
	w := newWorld(t)
	now := w.now()
	if ok, err := w.db.Leases().Acquire(context.Background(), types.Lease{
		Name: "storage-gc", Holder: "elsewhere", Acquired: now, Expires: now.Add(time.Hour),
	}, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if rep := run(t, w.collector(t, w.db)); !rep.Skipped {
		t.Fatalf("ran while another instance held the lease: %+v", rep)
	}
}

// racing wraps the blob index so a test can answer Get differently on each
// call, as a deploy referencing the blob mid-collection would.
type racing struct {
	*inmem.Store
	get func(n int) (types.Blob, error, bool)
	n   *atomic.Int64
}

func (r racing) Blobs() store.BlobIndex { return racingBlobs{r.Store.Blobs(), r} }
func (r racing) Tx(ctx context.Context, fn func(context.Context) error) error {
	return r.Store.Tx(ctx, fn) //nolint:wrapcheck // test double
}

type racingBlobs struct {
	store.BlobIndex
	r racing
}

func (b racingBlobs) Get(ctx context.Context, sha string) (types.Blob, error) {
	if got, err, ok := b.r.get(int(b.r.n.Add(1))); ok {
		return got, err
	}
	return b.BlobIndex.Get(ctx, sha) //nolint:wrapcheck // test double
}

func TestRaces(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		// get answers the Nth Get: 1 is inside the delete transaction, 2
		// before the content delete, 3 after it (the orphan walk follows).
		get                 func(n int) (types.Blob, error, bool)
		kept, deleted, lost int
		stored              bool
	}{
		"referenced before the row delete": {
			get: func(n int) (types.Blob, error, bool) {
				return types.Blob{Refs: 1}, nil, n == 1
			},
			kept: 1, stored: true,
		},
		"referenced after the row delete": {
			get: func(n int) (types.Blob, error, bool) {
				return types.Blob{}, nil, n == 2
			},
			kept: 1, stored: true,
		},
		"uploaded again during the content delete": {
			get: func(n int) (types.Blob, error, bool) {
				return types.Blob{}, nil, n == 3
			},
			deleted: 1, lost: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.trashOff(t)
			sha := w.deploy(t, "f.txt", "content").Actual.SHA256
			w.remove(t, "f.txt")
			w.advance(2 * gc.DefaultGrace)
			st := racing{w.db, tc.get, &atomic.Int64{}}
			rep, err := w.collector(t, st).Run(ctx)
			if err != nil || rep.Kept != tc.kept || rep.BlobsDeleted != tc.deleted ||
				len(rep.Lost) != tc.lost || w.stored(t, sha) != tc.stored {
				t.Fatalf("%+v, %v", rep, err)
			}
		})
	}
}

// failing makes chosen store calls fail.
type failing struct {
	*inmem.Store
	stale, unreferenced, acquire error
	renewals                     int
	calls                        *atomic.Int64
}

func (f failing) Uploads() store.UploadStore { return failUploads{f.Store.Uploads(), f.stale} }
func (f failing) Blobs() store.BlobIndex     { return failBlobs{f.Store.Blobs(), f.unreferenced} }
func (f failing) Leases() store.LeaseStore {
	return failLeases{f.Store.Leases(), f.acquire, f.renewals, f.calls}
}

type failUploads struct {
	store.UploadStore
	err error
}

func (f failUploads) Stale(ctx context.Context, before time.Time, n int) ([]store.Upload, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.UploadStore.Stale(ctx, before, n) //nolint:wrapcheck // test double
}

type failBlobs struct {
	store.BlobIndex
	err error
}

func (f failBlobs) Unreferenced(ctx context.Context, p store.Page) ([]types.Blob, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.BlobIndex.Unreferenced(ctx, p) //nolint:wrapcheck // test double
}

// failLeases fails Acquire with err, or refuses every Acquire after the
// first renewals ones, as a lease taken over mid-pass would.
type failLeases struct {
	store.LeaseStore
	err      error
	renewals int
	calls    *atomic.Int64
}

func (f failLeases) Acquire(ctx context.Context, l types.Lease, now time.Time) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.renewals > 0 && f.calls.Add(1) > int64(f.renewals) {
		return false, nil
	}
	return f.LeaseStore.Acquire(ctx, l, now) //nolint:wrapcheck // test double
}

var errBoom = errors.New("boom")

func TestStepFailures(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.deploy(t, "f.txt", "content")
	w.remove(t, "f.txt")
	w.advance(15 * 24 * time.Hour)
	rep, err := w.collector(t, failing{Store: w.db, stale: errBoom, unreferenced: errBoom}).Run(ctx)
	if !errors.Is(err, errBoom) || rep.TrashPurged != 1 {
		t.Fatalf("a failing step must not stop the others: %+v, %v", rep, err)
	}
	if _, err := w.collector(t, failing{Store: w.db, acquire: errBoom}).
		Run(ctx); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatalf("lease failure: %v", err)
	}
	_, err = w.collector(t, failing{Store: w.db, renewals: 1, calls: &atomic.Int64{}}).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "taken over") {
		t.Fatalf("a lease lost mid-pass stops the pass: %v", err)
	}
}

func TestLoop(t *testing.T) {
	w := newWorld(t)
	var buf bytes.Buffer
	c, err := gc.New(w.db, w.blobs, gc.Options{Log: slog.New(slog.NewTextHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan gc.Report, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Loop(ctx, time.Millisecond, func(r gc.Report, _ error) {
			select {
			case ran <- r:
			default:
			}
		})
	}()
	<-ran
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop ended with %v", err)
	}
}
