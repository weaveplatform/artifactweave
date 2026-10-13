package gc_test

import (
	"context"
	"errors"
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
	c, err := gc.New(st, w.blobs, gc.Options{Now: w.now})
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
	// The row stays a day as a tombstone, so a deploy of the same content
	// racing the removal finds out; then it is pruned.
	if b, err := w.db.Blobs().
		Get(context.Background(), sha); err != nil ||
		b.State != types.BlobDeleted {
		t.Fatalf("tombstone: %+v, %v", b, err)
	}
	w.advance(25 * time.Hour)
	if rep := run(t, c); rep.Pruned != 1 {
		t.Fatalf("prune: %+v", rep)
	}
	if _, err := w.db.Blobs().Get(context.Background(), sha); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tombstone left after a day: %v", err)
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

// TestReapers checks that a stale session of a backend with a reaper is
// handed to it, and that a reaper's failure fails the step.
func TestReapers(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	old := w.now().Add(-48 * time.Hour)
	if err := w.db.Uploads().Put(ctx, store.Upload{
		ID: "mp", Backend: "jfrog-multipart", Started: old, Updated: old,
	}); err != nil {
		t.Fatal(err)
	}
	var reaped []string
	boom := errors.New("boom")
	fail := true
	c, err := gc.New(
		w.db,
		w.blobs,
		gc.Options{Now: w.now, Reapers: map[string]func(context.Context, store.Upload) error{
			"jfrog-multipart": func(ctx context.Context, u store.Upload) error {
				if fail {
					return boom
				}
				reaped = append(reaped, u.ID)
				return w.db.Uploads().Delete(ctx, u.ID)
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(ctx); !errors.Is(err, boom) {
		t.Fatalf("a failing reaper fails the pass: %v", err)
	}
	fail = false
	if rep := run(t, c); rep.UploadsReaped != 1 || len(reaped) != 1 || reaped[0] != "mp" {
		t.Fatalf("reaped %v, %+v", reaped, rep)
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
	c, err := gc.New(w.db, w.blobs, gc.Options{})
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
