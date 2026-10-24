package mirror

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// clock is a settable time source shared by a mirror and its test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rig is a mirror over fakes with an in-memory metadata store providing the
// replica index, the blob index and the leases.
type rig struct {
	s   *Store
	f   []*fake
	db  *inmem.Store
	clk *clock
}

func (r *rig) options(ms []Member, tune func(*Options)) Options {
	o := Options{
		Members: ms, ChunkSize: testChunk, Sessions: session.NewMemory(),
		Replicas: r.db.Replicas(), Blobs: r.db.Blobs(), Leases: r.db.Leases(), Now: r.clk.now,
	}
	if tune != nil {
		tune(&o)
	}
	return o
}

func newRig(t *testing.T, n int, tune func(*Options)) *rig {
	t.Helper()
	f, ms := fakes(n, false)
	r := &rig{f: f, db: inmem.New(), clk: newClock()}
	r.s = r.mirror(t, ms, tune)
	return r
}

// mirror builds another mirror over ms sharing the rig's metadata store,
// which is how a configuration change (a member added or drained) looks.
func (r *rig) mirror(t *testing.T, ms []Member, tune func(*Options)) *Store {
	t.Helper()
	s, err := New(r.options(ms, tune))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (r *rig) repair(t *testing.T) RepairReport {
	t.Helper()
	rep, err := r.s.Repair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func (r *rig) record(t *testing.T, sha, member string) types.BlobReplica {
	t.Helper()
	rec, err := r.db.Replicas().Get(context.Background(), sha, member)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func (r *rig) setPending(t *testing.T, sha, member string) {
	t.Helper()
	rec := types.BlobReplica{
		SHA256: sha, Member: member, State: types.ReplicaPending, Updated: r.clk.now(),
	}
	if err := r.db.Replicas().Put(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

// putSettled stores body and waits for the members still committing in the
// background, so their late records and copies cannot race the test.
func putSettled(t *testing.T, s *Store, body []byte) blob.Info {
	t.Helper()
	info := put(t, s, body)
	if err := s.settle(context.Background(), info.Digests.SHA256); err != nil {
		t.Fatal(err)
	}
	return info
}

// contents reads a blob back from one fake member.
func contents(t *testing.T, f *fake, sha string) []byte {
	t.Helper()
	rc, _, err := f.Open(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnitRepairCopiesPendingReplica(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 3, nil)
	body := blobtest.Content(7, 3*testChunk+11)
	info := putSettled(t, r.s, body)
	sha := info.Digests.SHA256
	// The blob index knows every digest, so the copy is verified against
	// all three.
	if _, err := r.db.Blobs().Ref(ctx, types.Blob{Checksums: types.Checksums{
		SHA256: sha, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
	}, Size: info.Size}, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.f[2].Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}
	r.setPending(t, sha, "m2")

	rep := r.repair(t)
	if rep.Copied != 1 || rep.Failed != 0 || rep.Skipped {
		t.Fatalf("report %+v", rep)
	}
	if !bytes.Equal(contents(t, r.f[2], sha), body) {
		t.Fatal("the repaired copy differs from the original")
	}
	got := r.record(t, sha, "m2")
	if got.State != types.ReplicaPresent || got.Size != int64(len(body)) || got.LastError != "" {
		t.Fatalf("record %+v", got)
	}
	if again := r.repair(t); again.Copied != 0 {
		t.Fatalf("a second pass found more work: %+v", again)
	}
}

func TestUnitRepairFindsStraggler(t *testing.T) {
	r := newRig(t, 3, nil)
	sha := putSettled(t, r.s, []byte("landed late")).Digests.SHA256
	// The commit landed after its record said pending.
	r.setPending(t, sha, "m1")
	begins := r.f[1].calls["begin"]
	rep := r.repair(t)
	if rep.Found != 1 || rep.Copied != 0 {
		t.Fatalf("report %+v", rep)
	}
	if r.record(t, sha, "m1").State != types.ReplicaPresent {
		t.Fatal("the member's copy was not recognised")
	}
	if n := r.f[1].calls["begin"] - begins; n != 0 {
		t.Fatalf("copied anyway: %d uploads", n)
	}
}

func TestUnitRepairWithoutSourceBacksOffThenFails(t *testing.T) {
	r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1; o.RepairMaxAttempts = 2 })
	sha := blobtest.Digest([]byte("nowhere")).SHA256
	r.setPending(t, sha, "m0")

	if rep := r.repair(t); rep.Failed != 1 {
		t.Fatalf("report %+v", rep)
	}
	rec := r.record(t, sha, "m0")
	if rec.State != types.ReplicaPending || rec.Attempts != 1 || rec.LastError != noSource {
		t.Fatalf("after one attempt: %+v", rec)
	}
	// Inside the backoff nothing is tried.
	r.clk.advance(DefaultRepairBackoff / 2)
	if rep := r.repair(t); rep.Failed != 0 {
		t.Fatalf("retried inside the backoff: %+v", rep)
	}
	r.clk.advance(DefaultRepairBackoff)
	if rep := r.repair(t); rep.Failed != 1 {
		t.Fatalf("not retried after the backoff: %+v", rep)
	}
	if rec := r.record(t, sha, "m0"); rec.State != types.ReplicaFailed || rec.Attempts != 2 {
		t.Fatalf("after the last attempt: %+v", rec)
	}
	r.clk.advance(maxRepairBackoff)
	if rep := r.repair(t); rep.Failed != 0 {
		t.Fatalf("a failed replica is left for an operator: %+v", rep)
	}
}

func TestUnitRepairBackoffDoublesAndCaps(t *testing.T) {
	r := newRig(t, 1, nil)
	base := r.clk.now()
	rec := types.BlobReplica{Attempts: 3, Updated: base}
	r.clk.advance(4*DefaultRepairBackoff - time.Second)
	if r.s.due(rec) {
		t.Fatal("the third retry waits four backoffs")
	}
	r.clk.advance(time.Second)
	if !r.s.due(rec) {
		t.Fatal("due after four backoffs")
	}
	rec.Attempts = 40
	if !r.s.due(types.BlobReplica{Attempts: 40, Updated: r.clk.now().Add(-maxRepairBackoff)}) {
		t.Fatal("the backoff is capped")
	}
}

func TestUnitRepairCopyFailures(t *testing.T) {
	ctx := context.Background()
	for _, op := range []string{"begin", "append", "commit", "open"} {
		t.Run(op, func(t *testing.T) {
			r := newRig(t, 3, nil)
			sha := putSettled(t, r.s, []byte("copy fails at "+op)).Digests.SHA256
			if err := r.f[2].Delete(ctx, sha); err != nil {
				t.Fatal(err)
			}
			r.setPending(t, sha, "m2")
			if op == "open" {
				r.f[0].failOn("open", 0, errInjected)
				r.f[1].failOn("open", 0, errInjected)
			} else {
				r.f[2].failOn(op, 0, errInjected)
			}
			if rep := r.repair(t); rep.Failed != 1 || rep.Copied != 0 {
				t.Fatalf("report %+v", rep)
			}
			rec := r.record(t, sha, "m2")
			if rec.Attempts != 1 || !strings.Contains(rec.LastError, "injected") {
				t.Fatalf("record %+v", rec)
			}
			if op != "open" && op != "begin" && len(r.f[2].uploads) != 0 {
				t.Fatal("the failed copy's upload was not aborted")
			}
		})
	}
}

func TestUnitRepairDeferredDelete(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 3, nil)
	sha := putSettled(t, r.s, []byte("delete me later")).Digests.SHA256
	r.f[1].failOn("delete", 0, errInjected)
	if err := r.s.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}
	if r.record(t, sha, "m1").State != types.ReplicaDeleting || !r.f[1].has(sha) {
		t.Fatal("no tombstone for the member that could not delete")
	}
	if rep := r.repair(t); rep.Failed != 1 {
		t.Fatalf("still failing: %+v", rep)
	}
	if rec := r.record(t, sha, "m1"); rec.State != types.ReplicaDeleting || rec.Attempts != 1 {
		t.Fatalf("a failing delete keeps its tombstone: %+v", rec)
	}
	r.f[1].heal()
	r.clk.advance(DefaultRepairBackoff)
	if rep := r.repair(t); rep.Deleted != 1 {
		t.Fatalf("report %+v", rep)
	}
	if r.f[1].has(sha) {
		t.Fatal("the copy is still there")
	}
	if left := states(t, r.db.Replicas(), sha); len(left) != 0 {
		t.Fatalf("records left: %v", left)
	}
}

func TestUnitRepairDeleteNeverRemovesAReupload(t *testing.T) {
	ctx := context.Background()
	for _, held := range []bool{true, false} {
		r := newRig(t, 3, nil)
		info := putSettled(t, r.s, []byte("back again"))
		sha := info.Digests.SHA256
		r.f[1].failOn("delete", 0, errInjected)
		if err := r.s.Delete(ctx, sha); err != nil {
			t.Fatal(err)
		}
		r.f[1].heal()
		if !held {
			_ = r.f[1].Delete(ctx, sha)
		}
		// The blob was uploaded again: the item layer referenced it.
		if _, err := r.db.Blobs().Ref(ctx, types.Blob{
			Checksums: types.Checksums{SHA256: sha}, Size: info.Size,
		}, 1); err != nil {
			t.Fatal(err)
		}
		if rep := r.repair(t); rep.DeletesSkipped != 1 || rep.Deleted != 0 {
			t.Fatalf("held=%v: report %+v", held, rep)
		}
		want := types.ReplicaPresent
		if !held {
			want = types.ReplicaPending
		}
		if got := r.record(t, sha, "m1").State; got != want {
			t.Fatalf("held=%v: state %s, want %s", held, got, want)
		}
		if held && !r.f[1].has(sha) {
			t.Fatal("the re-uploaded copy was deleted")
		}
	}
}

func TestUnitRepairBackfillsAddedMember(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 3, nil)
	var shas []string
	for i := range 3 {
		shas = append(shas, putSettled(t, r.s, blobtest.Content(byte(i), 100+i)).Digests.SHA256)
	}
	added := newFake()
	_, ms := fakes(0, false)
	for i, f := range r.f {
		ms = append(ms, Member{Name: []string{"m0", "m1", "m2"}[i], Store: f, WriteEnabled: true})
	}
	ms = append(ms, Member{Name: "m3", Store: added, WriteEnabled: true})
	r.s = r.mirror(t, ms, nil)

	if rep := r.repair(t); rep.Backfilled != 3 {
		t.Fatalf("first pass: %+v", rep)
	}
	if rep := r.repair(t); rep.Copied != 3 {
		t.Fatalf("second pass: %+v", rep)
	}
	for _, sha := range shas {
		if !added.has(sha) || r.record(t, sha, "m3").State != types.ReplicaPresent {
			t.Fatalf("%s not on the added member", sha)
		}
	}
	if rep := r.repair(t); rep.Backfilled != 0 || rep.Copied != 0 {
		t.Fatalf("converged mirror still works: %+v", rep)
	}
	_ = ctx
}

func TestUnitRepairDrainsWriteDisabledMember(t *testing.T) {
	r := newRig(t, 2, func(o *Options) { o.Redundancy = 1; o.WriteQuorum = 1 })
	sha := putSettled(t, r.s, []byte("only one copy")).Digests.SHA256
	recs := states(t, r.db.Replicas(), sha)
	if len(recs) != 1 {
		t.Fatalf("redundancy 1 placed %v", recs)
	}
	var holder, other int
	for name := range recs {
		holder = map[string]int{"m0": 0, "m1": 1}[name]
	}
	other = 1 - holder
	ms := []Member{
		{Name: "m0", Store: r.f[0], WriteEnabled: holder != 0},
		{Name: "m1", Store: r.f[1], WriteEnabled: holder != 1},
	}
	r.s = r.mirror(t, ms, func(o *Options) { o.Redundancy = 1; o.WriteQuorum = 1 })
	if rep := r.repair(t); rep.Drained != 1 || rep.Backfilled != 0 {
		t.Fatalf("first pass: %+v", rep)
	}
	if rep := r.repair(t); rep.Copied != 1 {
		t.Fatalf("second pass: %+v", rep)
	}
	if !r.f[other].has(sha) {
		t.Fatal("not copied off the draining member")
	}
	if !r.f[holder].has(sha) {
		t.Fatal("a drain must never delete the original copy")
	}
}

func TestUnitRepairSkipsDeletedAndUncopiedBlobs(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 3, nil)
	sha := blobtest.Digest([]byte("gone")).SHA256
	_ = r.db.Replicas().Put(ctx, types.BlobReplica{
		SHA256: sha, Member: "m0", State: types.ReplicaDeleting, Updated: r.clk.now(),
	})
	pendingOnly := blobtest.Digest([]byte("pending only")).SHA256
	r.setPending(t, pendingOnly, "m1")
	r.f[0].failOn("delete", 0, errInjected)
	rep := r.repair(t)
	if rep.Backfilled != 0 {
		t.Fatalf("backfilled a deleted or never-stored blob: %+v", rep)
	}
}

func TestUnitRepairLease(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 3, nil)
	sha := putSettled(t, r.s, []byte("leased")).Digests.SHA256
	_ = r.f[2].Delete(ctx, sha)
	r.setPending(t, sha, "m2")
	now := r.clk.now()
	if ok, err := r.db.Leases().Acquire(ctx, types.Lease{
		Name: defaultRepairLease, Holder: "elsewhere", Acquired: now, Expires: now.Add(time.Hour),
	}, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	rep, err := r.s.Repair(ctx)
	if err != nil || !rep.Skipped || rep.Copied != 0 {
		t.Fatalf("Repair = %+v, %v", rep, err)
	}
	if r.record(t, sha, "m2").State != types.ReplicaPending {
		t.Fatal("touched a replica without the lease")
	}
	// Two passes in one process share a holder; the second is skipped.
	r.s.repair.running.Lock()
	rep, err = r.s.Repair(ctx)
	r.s.repair.running.Unlock()
	if err != nil || !rep.Skipped {
		t.Fatalf("concurrent pass = %+v, %v", rep, err)
	}
	if err := r.db.Leases().Release(ctx, defaultRepairLease, "elsewhere"); err != nil {
		t.Fatal(err)
	}
	if rep := r.repair(t); rep.Copied != 1 {
		t.Fatalf("after release: %+v", rep)
	}
	leases, err := r.db.Leases().List(ctx)
	if err != nil || len(leases) != 0 {
		t.Fatalf("the pass did not release its lease: %v %v", leases, err)
	}
}

// TestUnitRepairLeaseName checks that a mirror with its own lease name repairs
// while the default lease is held, as the cold tier must beside the hot one.
func TestUnitRepairLeaseName(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 3, func(o *Options) { o.RepairLease = "cold-blob-mirror-repair" })
	sha := putSettled(t, r.s, []byte("own lease")).Digests.SHA256
	_ = r.f[2].Delete(ctx, sha)
	r.setPending(t, sha, "m2")
	now := r.clk.now()
	if ok, err := r.db.Leases().Acquire(ctx, types.Lease{
		Name: defaultRepairLease, Holder: "hot tier", Acquired: now, Expires: now.Add(time.Hour),
	}, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if rep := r.repair(t); rep.Skipped || rep.Copied != 1 {
		t.Fatalf("repair under its own lease = %+v", rep)
	}
}

func TestUnitRunRepair(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu     sync.Mutex
			passes int
		)
		db := inmem.New()
		_, ms := fakes(2, false)
		s, err := New(Options{
			Members: ms, WriteQuorum: 1, Sessions: session.NewMemory(), Replicas: db.Replicas(),
			Blobs: db.Blobs(), Leases: db.Leases(),
			OnRepair: func(rep RepairReport, err error) {
				mu.Lock()
				defer mu.Unlock()
				if err == nil && !rep.Skipped {
					passes++
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		trigger := make(chan struct{})
		done := make(chan error, 1)
		go func() { done <- s.RunRepair(ctx, 0, trigger) }()
		count := func() int {
			synctest.Wait()
			mu.Lock()
			defer mu.Unlock()
			return passes
		}
		if n := count(); n != 0 {
			t.Fatalf("ran before the interval: %d", n)
		}
		trigger <- struct{}{}
		if n := count(); n != 1 {
			t.Fatalf("trigger: %d passes", n)
		}
		time.Sleep(DefaultRepairInterval)
		if n := count(); n != 2 {
			t.Fatalf("interval: %d passes", n)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("RunRepair = %v", err)
		}
	})
}

// repairReplicas is a replica index whose List, Delete and Put fail on
// demand.
type repairReplicas struct {
	store.ReplicaIndex
	list, del, put error
}

func (f repairReplicas) List(
	ctx context.Context, rf store.ReplicaFilter, p store.Page,
) ([]types.BlobReplica, string, error) {
	if f.list != nil {
		return nil, "", f.list
	}
	return f.ReplicaIndex.List(ctx, rf, p) //nolint:wrapcheck // test wrapper
}

func (f repairReplicas) Delete(ctx context.Context, sha, member string) error {
	if f.del != nil {
		return f.del
	}
	return f.ReplicaIndex.Delete(ctx, sha, member) //nolint:wrapcheck // test wrapper
}

func (f repairReplicas) Put(ctx context.Context, r types.BlobReplica) error {
	if f.put != nil {
		return f.put
	}
	return f.ReplicaIndex.Put(ctx, r) //nolint:wrapcheck // test wrapper
}

// failingLeases fails Acquire.
type failingLeases struct{ store.LeaseStore }

func (failingLeases) Acquire(context.Context, types.Lease, time.Time) (bool, error) {
	return false, errInjected
}

// failingBlobs fails Get with a driver error.
type failingBlobs struct{ store.BlobIndex }

func (failingBlobs) Get(context.Context, string) (types.Blob, error) {
	return types.Blob{}, errInjected
}

func TestUnitRepairErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("not configured", func(t *testing.T) {
		_, ms := fakes(2, false)
		s, _ := newMirror(t, ms, nil)
		if _, err := s.Repair(ctx); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	})
	t.Run("bad settings", func(t *testing.T) {
		_, ms := fakes(2, false)
		for _, tune := range []func(*Options){
			func(o *Options) { o.RepairBatch = -1 },
			func(o *Options) { o.RepairMaxAttempts = -1 },
			func(o *Options) { o.RepairBackoff = -time.Second },
		} {
			o := Options{
				Members:  ms,
				Sessions: session.NewMemory(),
				Replicas: inmem.New().Replicas(),
			}
			tune(&o)
			if _, err := New(o); !errors.Is(err, ErrConfig) {
				t.Fatalf("New = %v", err)
			}
		}
	})
	t.Run("lease fails", func(t *testing.T) {
		r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1 })
		r.s.repair.leases = failingLeases{r.db.Leases()}
		if _, err := r.s.Repair(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("list fails", func(t *testing.T) {
		r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1 })
		r.s.replicas = repairReplicas{ReplicaIndex: r.db.Replicas(), list: errInjected}
		if _, err := r.s.Repair(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("blob index fails", func(t *testing.T) {
		r := newRig(t, 3, nil)
		sha := putSettled(t, r.s, []byte("index down")).Digests.SHA256
		r.f[1].failOn("delete", 0, errInjected)
		if err := r.s.Delete(ctx, sha); err != nil {
			t.Fatal(err)
		}
		r.s.repair.blobs = failingBlobs{r.db.Blobs()}
		if _, err := r.s.Repair(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("record writes fail", func(t *testing.T) {
		r := newRig(t, 3, nil)
		sha := putSettled(t, r.s, []byte("unrecordable")).Digests.SHA256
		_ = r.f[2].Delete(ctx, sha)
		r.setPending(t, sha, "m2")
		r.s.replicas = repairReplicas{ReplicaIndex: r.db.Replicas(), put: errInjected}
		if _, err := r.s.Repair(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("tombstone cleanup fails", func(t *testing.T) {
		r := newRig(t, 3, nil)
		sha := putSettled(t, r.s, []byte("stubborn record")).Digests.SHA256
		r.f[1].failOn("delete", 0, errInjected)
		if err := r.s.Delete(ctx, sha); err != nil {
			t.Fatal(err)
		}
		r.f[1].heal()
		r.s.replicas = repairReplicas{ReplicaIndex: r.db.Replicas(), del: errInjected}
		if _, err := r.s.Repair(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("source breaker open", func(t *testing.T) {
		r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1; o.BreakerThreshold = 1 })
		sha := putSettled(t, r.s, []byte("source unhealthy")).Digests.SHA256
		_ = r.f[1].Delete(ctx, sha)
		r.setPending(t, sha, "m1")
		r.s.byName["m0"].health.failure(r.clk.now(), 1, time.Hour)
		if rep := r.repair(t); rep.Failed != 1 {
			t.Fatalf("report %+v", rep)
		}
		if rec := r.record(t, sha, "m1"); rec.LastError != noSource {
			t.Fatalf("record %+v", rec)
		}
	})
	t.Run("removed member and open target", func(t *testing.T) {
		r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1; o.BreakerThreshold = 1 })
		sha := putSettled(t, r.s, []byte("skipped")).Digests.SHA256
		r.setPending(t, sha, "gone")
		_ = r.f[1].Delete(ctx, sha)
		r.setPending(t, sha, "m1")
		r.s.byName["m1"].health.failure(r.clk.now(), 1, time.Hour)
		if rep := r.repair(t); rep.Copied+rep.Failed+rep.Found != 0 {
			t.Fatalf("worked on a removed or unhealthy member: %+v", rep)
		}
	})
}

func TestUnitRepairBatches(t *testing.T) {
	r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1; o.RepairBatch = 2 })
	var shas []string
	for i := range 5 {
		sha := putSettled(t, r.s, blobtest.Content(byte(i), 50)).Digests.SHA256
		_ = r.f[1].Delete(context.Background(), sha)
		r.setPending(t, sha, "m1")
		shas = append(shas, sha)
	}
	total := 0
	for range 3 {
		rep := r.repair(t)
		if rep.Copied > 2 {
			t.Fatalf("a pass exceeded its batch: %+v", rep)
		}
		total += rep.Copied
	}
	if total != 5 {
		t.Fatalf("copied %d of 5 over three passes", total)
	}
	for _, sha := range shas {
		if !r.f[1].has(sha) {
			t.Fatalf("%s not repaired", sha)
		}
	}
}

// onceLeases grants the lease on the first Acquire only, as if another
// process took it over after the pass started.
type onceLeases struct {
	store.LeaseStore
	calls *int
}

func (o onceLeases) Acquire(ctx context.Context, l types.Lease, now time.Time) (bool, error) {
	*o.calls++
	if *o.calls > 1 {
		return false, nil
	}
	return o.LeaseStore.Acquire(ctx, l, now) //nolint:wrapcheck // test wrapper
}

func TestUnitRepairStopsWhenLeaseIsTakenOver(t *testing.T) {
	r := newRig(t, 2, func(o *Options) { o.WriteQuorum = 1 })
	calls := 0
	r.s.repair.leases = onceLeases{LeaseStore: r.db.Leases(), calls: &calls}
	if _, err := r.s.Repair(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "taken over") {
		t.Fatalf("Repair = %v", err)
	}
}
