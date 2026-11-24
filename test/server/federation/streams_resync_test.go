package federation_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

const snapshotPath = eventsPath + testStream + "/snapshot"

// runs is in's full-sync runs, newest first.
func (in *streamInstance) runs() []model.FullSyncRun {
	in.t.Helper()
	runs, _, err := in.runtime.Store.Federation().FullSyncs(in.t.Context(), "", kernel.Page{})
	if err != nil {
		in.t.Fatal(err)
	}
	return runs
}

func (in *streamInstance) flags() []model.StreamResync {
	in.t.Helper()
	f, err := in.runtime.Store.Resyncs().Resyncs(in.t.Context(), "")
	if err != nil {
		in.t.Fatal(err)
	}
	return f
}

// pruneLater prunes in's stream as it would once the retention window
// has passed: an engine of its own, so the running one's clock is left
// alone.
func (in *streamInstance) pruneLater() {
	in.t.Helper()
	e := in.engine
	later := &streamsync.Engine{
		Store: e.Store, Kinds: e.Kinds, Blobs: e.Blobs, Remote: e.Remote, Queue: e.Queue, Batch: e.Batch,
		Now: func() time.Time { return time.Now().Add(streamsync.DefaultRetention + time.Hour) },
	}
	if err := later.Prune(in.t.Context()); err != nil {
		in.t.Fatal(err)
	}
}

// waitSendIdle waits until no run of in's dispatcher to peer is under way.
func (in *streamInstance) waitSendIdle(peer *streamInstance) {
	in.t.Helper()
	key := peer.id + " " + testStream
	var state work.WorkState
	eventually(in.t, func() string { return fmt.Sprint("A's dispatcher to B is ", state) }, func() bool {
		it, err := in.runtime.Store.WorkQueue().Get(in.t.Context(), streamsync.SendKind, key)
		if errors.Is(err, kernel.ErrNotFound) {
			return true
		}
		if err != nil {
			in.t.Fatal(err)
		}
		state = it.State
		return it.State != work.WorkLeased
	})
}

// waitResynced waits until in holds origin's items as origin does, with
// the same stamps, its run has completed and neither side flags the
// stream.
func (in *streamInstance) waitResynced(origin *streamInstance) model.FullSyncRun {
	in.t.Helper()
	describe := func() string {
		return fmt.Sprintf("items %v, want %v; runs %+v; flags %+v and %+v",
			in.state(origin).Items, origin.state(origin).Items, in.runs(), in.flags(), origin.flags())
	}
	eventually(in.t, describe, func() bool {
		got, want := in.state(origin), origin.state(origin)
		runs := in.runs()
		return fmt.Sprint(got.Items) == fmt.Sprint(want.Items) && fmt.Sprint(got.Stamps) == fmt.Sprint(want.Stamps) &&
			len(runs) > 0 && runs[0].Status == retentionapi.RunCompleted &&
			len(in.flags()) == 0 && len(origin.flags()) == 0
	})
	return in.runs()[0]
}

func testStreamResync(t *testing.T, driver string, dsn func() string) {
	t.Run("stopped past retention", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		for i := range 3 {
			a.put(fmt.Sprint("k", i), "first")
		}
		b.waitApplied(a, 3)
		eventually(t, func() string { return fmt.Sprint("cursor ", a.cursor(b)) }, func() bool { return a.cursor(b) == 3 })

		// B stops; A writes, deletes and adds content, and its window
		// passes, so it prunes what B never applied and flags B.
		b.setHook(func(w http.ResponseWriter, _ *http.Request, _ http.Handler) bool {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		})
		for i := range 5 {
			a.put(fmt.Sprint("k", i), "second")
		}
		a.commit(streamtest.Change{Type: streamtest.Delete, Key: "k1"})
		body, sum := blobContent("written while B was away")
		a.commit(streamtest.Change{Type: streamtest.Put, Key: "blob", Content: body})
		a.pruneLater()
		if f := a.flags(); len(f) != 1 || f[0].Receiver != b.id || f[0].Position != 3 {
			t.Fatalf("A's flags %+v, want B at 3", f)
		}
		// A push already under way read B's flag before the prune set it.
		// B must not take it: B would then hold every entry and need no
		// full sync, which is not the recovery this scenario shows (the
		// sync package's TestUnitDispatchClearsAFlagItsPushOutran covers
		// that one). So B stays away until A's dispatcher is idle; any run
		// after that reads the flag.
		a.waitSendIdle(b)

		// B is back: A's next change finds B behind the floor and is
		// pushed to it; B refuses it, asks for what it lacks, finds it
		// gone, and recovers from a snapshot.
		b.setHook(nil)
		a.put("after", "the prune")
		run := b.waitResynced(a)
		if run.PeerInstanceID != a.id || run.Stream != testStream || run.Forced || run.SnapshotSeq != 11 {
			t.Fatalf("run %+v, want A's stream from sequence 11", run)
		}
		if ok, err := b.runtime.Blobs.Exists(t.Context(), sum); err != nil || !ok {
			t.Fatalf("the snapshot's content: %v, %v", ok, err)
		}

		// And continues: A's next entry is pushed and applied.
		a.put("later", "pushed")
		eventually(t, func() string { return fmt.Sprint(b.state(a).Items) }, func() bool {
			return b.state(a).Items["later"].Value == "pushed"
		})
		eventually(t, func() string { return fmt.Sprint("cursor ", a.cursor(b)) }, func() bool { return a.cursor(b) == 12 })
	})

	t.Run("snapshot while writing", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		for i := range 12 {
			a.put(fmt.Sprintf("k%02d", i), "first")
		}
		b.waitApplied(a, 12)

		// Every page A serves is followed by a write: a key the snapshot
		// holds changes, one goes and a new one comes.
		var (
			mu     sync.Mutex
			served int
			errs   []error
		)
		write := func(c streamtest.Change) {
			if _, err := a.kind.Commit(t.Context(), a.engine, testStream, c, nil); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}
		a.setHook(func(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
			if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, snapshotPath) {
				return false
			}
			next.ServeHTTP(w, r)
			mu.Lock()
			served++
			n := served
			mu.Unlock()
			write(streamtest.Change{Type: streamtest.Put, Key: fmt.Sprintf("k%02d", 11-n), Value: "during"})
			write(streamtest.Change{Type: streamtest.Delete, Key: fmt.Sprintf("k%02d", n)})
			write(streamtest.Change{Type: streamtest.Put, Key: fmt.Sprint("new", n), Value: "during"})
			return true
		})

		// The pages read directly all state the snapshot's sequence, and
		// together hold the stream as it was then.
		want := a.state(a)
		peer := b.peer(a.id)
		var (
			keys   []string
			cursor string
			seq    int64 = -1
		)
		for {
			page, err := b.engine.Remote.Snapshot(t.Context(), peer, testStream, cursor, 5)
			if err != nil {
				t.Fatal(err)
			}
			if seq >= 0 && page.Sequence != seq {
				t.Fatalf("a page at sequence %d, the first at %d", page.Sequence, seq)
			}
			seq = page.Sequence
			for _, en := range page.Entries {
				keys = append(keys, en.Key)
				if st := want.Stamps[en.Key]; st.HLC != en.Time.String() || st.Deleted != en.Deleted {
					t.Fatalf("entry %+v, want the stamp %+v it had at the snapshot", en, st)
				}
			}
			if cursor = page.NextCursor; cursor == "" {
				break
			}
		}
		if seq != 12 || len(keys) != 12 || keys[0] != "k00" || keys[11] != "k11" {
			t.Fatalf("snapshot at %d of %v, want k00 to k11 at 12", seq, keys)
		}

		// A full sync while A goes on writing converges, the writes after
		// its sequence applied from the stream.
		if _, err := b.engine.FullSync(t.Context(), a.id, testStream, false); err != nil {
			t.Fatal(err)
		}
		b.waitResynced(a)
		mu.Lock()
		defer mu.Unlock()
		if len(errs) > 0 || served < 4 {
			t.Fatalf("A served %d pages; writes failed: %v", served, errs)
		}
	})

	t.Run("snapshot refusals", func(t *testing.T) {
		a, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
		a.put("k", "v")
		b.waitApplied(a, 1)
		// Without the stream's use case, 403; a cursor whose snapshot is
		// gone, 410; unpaired, 401.
		b.record(a, []model.UseCase{model.UseFederatedRepo}, model.DeliveryPush)
		a.record(b, []model.UseCase{model.UseDistribution}, model.DeliveryPush)
		if _, err := b.engine.Remote.Snapshot(t.Context(), b.peer(a.id), testStream, "", 5); err == nil ||
			!strings.Contains(err.Error(), "403") {
			t.Fatalf("snapshot without the use case: %v, want 403", err)
		}
		a.record(b, []model.UseCase{model.UseFederatedRepo}, model.DeliveryPush)
		page, err := b.engine.Remote.Snapshot(t.Context(), b.peer(a.id), testStream, "", 5)
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("snapshot: %+v, %v", page, err)
		}
		// {"s":999,"k":""}, a snapshot A never took.
		if _, err := b.engine.Remote.Snapshot(t.Context(), b.peer(a.id), testStream, "eyJzIjo5OTksImsiOiIifQ", 5); !errors.Is(err, streamsync.ErrGone) {
			t.Fatalf("a gone snapshot's cursor: %v, want 410", err)
		}
		if err := a.runtime.Store.Peers().Delete(t.Context(), b.id); err != nil {
			t.Fatal(err)
		}
		if _, err := b.engine.Remote.Snapshot(t.Context(), b.peer(a.id), testStream, "", 5); err == nil ||
			!strings.Contains(err.Error(), "401") {
			t.Fatalf("unpaired snapshot: %v, want 401", err)
		}
	})
}

func TestAccStreamResyncSQLite(t *testing.T) {
	t.Parallel()
	testStreamResync(t, "", func() string { return "" })
}

func TestAccStreamResyncPostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testStreamResync(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
