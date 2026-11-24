package sync_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// runs is n's full-sync runs, newest first.
func (n *node) runs() []model.FullSyncRun {
	n.t.Helper()
	runs, _, err := n.store.Federation().FullSyncs(n.t.Context(), "", kernel.Page{})
	if err != nil {
		n.t.Fatal(err)
	}
	return runs
}

// converged fails unless n holds of origin's stream exactly what origin
// holds of its own: the same items and the same last change of every key.
func converged(t *testing.T, n, origin *node) {
	t.Helper()
	want, got := origin.state(origin), n.state(origin)
	if fmt.Sprint(got.Items) != fmt.Sprint(want.Items) {
		t.Fatalf("items %v, want %v", got.Items, want.Items)
	}
	if fmt.Sprint(got.Stamps) != fmt.Sprint(want.Stamps) {
		t.Fatalf("stamps %v, want %v", got.Stamps, want.Stamps)
	}
}

// behind leaves b behind a's pruned floor: b stops, a writes keys,
// deletes and content, and a's window passes, so a prunes every entry b
// has not applied and flags it.
func behind(t *testing.T, w *world, a, b *node) {
	t.Helper()
	for i := range 4 {
		a.put(fmt.Sprint("k", i), "first")
	}
	a.run(streamsync.SendKind)
	w.faults.down[b.id] = true
	for i := range 7 {
		a.put(fmt.Sprint("k", i), "second")
	}
	a.commit(streamtest.Change{Type: streamtest.Delete, Key: "k1"})
	body, _ := content("content written while B was away")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "blob", Content: body})
	a.run(streamsync.SendKind)
	*a.clock = t0.Add(streamsync.DefaultRetention + time.Hour)
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f := a.resyncs(); len(f) != 1 || f[0].Receiver != b.id {
		t.Fatalf("A's flags %+v, want B", f)
	}
	delete(w.faults.down, b.id)
}

func TestUnitFullSyncRecoversPushReceiverPastTheWindow(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	// Small pages, so the snapshot takes several.
	b.engine.Batch = 2
	behind(t, w, a, b)

	// A's next change finds B behind the floor: A pushes it the first
	// entry it holds, which B refuses and probes.
	a.put("after", "the prune")
	a.run(streamsync.SendKind)
	if got := b.applied(a); len(got) != 4 {
		t.Fatalf("B applied %v across the gap", got)
	}
	b.run(streamsync.FullSyncKind)
	converged(t, b, a)
	if w.snapshots < 4 {
		t.Fatalf("%d snapshot pages, want several", w.snapshots)
	}
	runs := b.runs()
	head := must(a.store.Outbox().LastSeq(t.Context(), a.id, stream))
	if len(runs) != 1 || runs[0].Status != retentionapi.RunCompleted || runs[0].SnapshotSeq != head ||
		runs[0].Stream != stream || runs[0].PeerInstanceID != a.id || runs[0].Finished.IsZero() {
		t.Fatalf("runs %+v, want one completed at %d", runs, head)
	}
	if len(a.resyncs()) != 0 || len(b.resyncs()) != 0 {
		t.Fatalf("flags A %+v B %+v after the full sync", a.resyncs(), b.resyncs())
	}
	if a.cursor(b) != head {
		t.Fatalf("A's cursor for B %d, want %d", a.cursor(b), head)
	}

	// The stream then continues from the snapshot's sequence.
	a.put("later", "pushed")
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a)[4:], []string{fmt.Sprint(head + 1)})
	converged(t, b, a)
}

func TestUnitFullSyncRecoversPullReceiverOn410(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPulled, model.DeliveryPull)
	b.engine.Batch = 2
	for i := range 3 {
		a.put(fmt.Sprint("k", i), "first")
	}
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	wantApplied(t, b.applied(a), seqs(1, 3))
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	for i := range 5 {
		a.put(fmt.Sprint("k", i), "second")
	}
	*a.clock = t0.Add(streamsync.DefaultRetention + time.Hour)
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	// B's pull is answered 410, which starts the full sync.
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	if f := b.resyncs(); len(f) != 1 || f[0].Run == 0 {
		t.Fatalf("B's flags %+v, want one naming its run", f)
	}
	b.run(streamsync.FullSyncKind)
	converged(t, b, a)
	if runs := b.runs(); len(runs) != 1 || runs[0].Status != retentionapi.RunCompleted {
		t.Fatalf("runs %+v", runs)
	}
	a.put("later", "pulled")
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	converged(t, b, a)
}

func TestUnitSnapshotPagesStayAtTheirSequence(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	for i := range 5 {
		a.put(fmt.Sprint("k", i), "v1")
	}
	caller := a.record(b.id)
	first := must(a.engine.Snapshot(t.Context(), caller, stream, "", 2))
	if first.Sequence != 5 || first.Origin != a.id || first.Stream != stream || len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	// Writes after the snapshot change neither its sequence nor its pages.
	a.put("k3", "v2")
	a.commit(streamtest.Change{Type: streamtest.Delete, Key: "k4"})
	a.put("k9", "new")
	var keys []string
	for _, en := range first.Entries {
		keys = append(keys, en.Key)
	}
	for cursor := first.NextCursor; cursor != ""; {
		page := must(a.engine.Snapshot(t.Context(), caller, stream, cursor, 2))
		if page.Sequence != 5 {
			t.Fatalf("page at %d, want the snapshot's 5", page.Sequence)
		}
		for _, en := range page.Entries {
			keys = append(keys, en.Key)
			if en.Key == "k3" && string(en.Payload) != `{"value":"v1"}` || en.Deleted {
				t.Fatalf("entry %+v is not as the snapshot took it", en)
			}
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(keys) != "[k0 k1 k2 k3 k4]" {
		t.Fatalf("keys %v", keys)
	}
	if f := a.resyncs(); len(f) != 1 || f[0].Receiver != b.id {
		t.Fatalf("taking a snapshot flagged %+v, want B", f)
	}
}

func TestUnitFullSyncConvergesWhileTheSenderWrites(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	b.engine.Batch = 2
	behind(t, w, a, b)
	writes := 0
	w.onSnapshot = func(cursor string) {
		if cursor == "" {
			return
		}
		// Between pages A changes keys already served and keys still to
		// come, and adds one.
		writes++
		a.put("k0", fmt.Sprint("during ", writes))
		a.put("k6", fmt.Sprint("during ", writes))
		a.put(fmt.Sprint("new", writes), "x")
		if writes == 2 {
			a.commit(streamtest.Change{Type: streamtest.Delete, Key: "k5"})
		}
	}
	a.put("after", "the prune")
	a.run(streamsync.SendKind)
	b.run(streamsync.FullSyncKind)
	w.onSnapshot = nil
	if writes == 0 {
		t.Fatal("nothing was written while the snapshot was paged")
	}
	a.run(streamsync.SendKind)
	converged(t, b, a)
}

func TestUnitFullSyncResumesAfterARestart(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	b.engine.Batch = 2
	behind(t, w, a, b)
	taken := 0
	w.onSnapshot = func(cursor string) {
		if cursor == "" {
			taken++
			return
		}
		// A becomes unreachable after B has applied the first page, as
		// if B stopped mid-run.
		w.mu.Lock()
		w.faults.down[a.id] = true
		w.mu.Unlock()
	}
	run := must(b.engine.FullSync(t.Context(), a.id, stream, false))
	if err := b.engine.Resync(t.Context(), a.id, stream); err == nil {
		t.Fatal("the interrupted run reported success")
	}
	mid := must(b.store.Federation().FullSync(t.Context(), run.ID))
	if mid.Phase != model.FullSyncSnapshot || mid.Cursor == "" || mid.LastKey == "" || mid.Message == "" ||
		mid.Status != retentionapi.RunInProgress {
		t.Fatalf("interrupted run %+v, want its position and the failure", mid)
	}
	// Restarted: an engine over the same store carries on from the run's
	// record, without taking the snapshot again.
	w.onSnapshot = func(cursor string) {
		if cursor == "" {
			taken++
		}
	}
	delete(w.faults.down, a.id)
	restarted := &streamsync.Engine{
		Store: b.engine.Store, Kinds: b.engine.Kinds, Blobs: b.engine.Blobs, Remote: b.engine.Remote,
		Queue: b.engine.Queue, Batch: b.engine.Batch, Now: b.engine.Now,
	}
	if err := restarted.Resync(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
	b.run(streamsync.FullSyncKind)
	if taken != 1 {
		t.Fatalf("the snapshot was taken %d times, want once", taken)
	}
	converged(t, b, a)
	if got := must(b.store.Federation().FullSync(t.Context(), run.ID)); got.Status != retentionapi.RunCompleted || got.Message != "" {
		t.Fatalf("run %+v", got)
	}
}

func TestUnitFullSyncForcedDiscardsDivergence(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprint("forced=", forced), func(t *testing.T) {
			w := newWorld()
			a, b := w.add(t), w.add(t)
			pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
			a.put("same", "1")
			a.put("older", "a's")
			a.run(streamsync.SendKind)
			// B holds a later change of "older" than A's, which wins under
			// the conflict rule, and a key A never had.
			later := outbox.HLC{Wall: t0.Add(time.Hour).UnixMilli()}
			if err := b.kind.Diverge(t.Context(), a.id, stream, "older", "b's", later); err != nil {
				t.Fatal(err)
			}
			if err := b.kind.Diverge(t.Context(), a.id, stream, "zz-only-b", "b's", later); err != nil {
				t.Fatal(err)
			}
			run := must(b.engine.FullSync(t.Context(), a.id, stream, forced))
			if run.Forced != forced || run.Status != retentionapi.RunInProgress {
				t.Fatalf("run %+v", run)
			}
			b.run(streamsync.FullSyncKind)
			items := b.state(a).Items
			if forced {
				converged(t, b, a)
				return
			}
			if items["older"].Value != "b's" || items["zz-only-b"].Value != "b's" || items["same"].Value != "1" {
				t.Fatalf("normal mode left %v, want B's winning and only items kept", items)
			}
			// A later change on A still applies over B's.
			a.put("older", "a's again")
			a.run(streamsync.SendKind)
			if got := b.state(a).Items["older"].Value; got != "a's again" {
				t.Fatalf("older is %q after A's next change", got)
			}
		})
	}
}

func TestUnitFullSyncForcedRestartsARunningOne(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	a.put("k", "v")
	a.run(streamsync.SendKind)
	first := must(b.engine.FullSync(t.Context(), a.id, stream, false))
	again := must(b.engine.FullSync(t.Context(), a.id, stream, false))
	if again.ID != first.ID {
		t.Fatalf("a second request started run %d beside %d", again.ID, first.ID)
	}
	forced := must(b.engine.FullSync(t.Context(), a.id, stream, true))
	if forced.ID != first.ID || !forced.Forced || forced.Phase != model.FullSyncSnapshot {
		t.Fatalf("forced request gave %+v", forced)
	}
	b.run(streamsync.FullSyncKind)
	if runs := b.runs(); len(runs) != 1 || runs[0].Status != retentionapi.RunCompleted || !runs[0].Forced {
		t.Fatalf("runs %+v", runs)
	}
	// Done, a new request starts a new run.
	if next := must(b.engine.FullSync(t.Context(), a.id, stream, false)); next.ID == first.ID {
		t.Fatal("a finished run was reused")
	}
}

func TestUnitFullSyncFailedFetchCompletesWithErrors(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	body, sum := content("fetched later")
	a.commit(streamtest.Change{Type: streamtest.Put, Key: "f", Content: body})
	a.put("k", "v")
	w.faults.fetchErr = errors.New("unavailable")
	must(b.engine.FullSync(t.Context(), a.id, stream, false))
	b.run(streamsync.FullSyncKind)
	if runs := b.runs(); len(runs) != 1 || runs[0].Status != retentionapi.RunCompletedWithErrors {
		t.Fatalf("runs %+v", runs)
	}
	if it := b.state(a).Items["f"]; !it.Pending || it.SHA256 != sum || len(b.failed()) != 1 {
		t.Fatalf("item %+v, failed %+v", it, b.failed())
	}
	w.faults.fetchErr = nil
	b.run(streamsync.FetchKind)
	converged(t, b, a)
}

func TestUnitFullSyncHoldsBackPushesUntilDone(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	a.put("k", "1")
	a.run(streamsync.SendKind)
	must(b.engine.FullSync(t.Context(), a.id, stream, false))
	a.put("k", "2")
	entries := must(a.store.Outbox().Read(t.Context(), a.id, stream, 1, 10))
	if _, err := b.engine.Receive(t.Context(), b.record(a.id), streamsync.Batch{
		Origin: a.id, Stream: stream, FromSeq: 2, Entries: entries,
	}); !errors.Is(err, streamsync.ErrBusy) {
		t.Fatalf("push during the full sync: %v, want busy", err)
	}
	// B pulls nothing either while it resyncs.
	put(t, b, a, fed, model.DeliveryPull)
	if err := b.engine.PullFrom(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
	wantApplied(t, b.applied(a), seqs(1, 1))
	put(t, b, a, fed, model.DeliveryPush)
	// Once the snapshot is taken A does not push to B either.
	b.run(streamsync.FullSyncKind)
	a.run(streamsync.SendKind)
	converged(t, b, a)
	a.put("k", "3")
	a.run(streamsync.SendKind)
	converged(t, b, a)
}

func TestUnitSweepRestartsFlaggedRuns(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	a.put("k", "v")
	w.faults.down[a.id] = true
	must(b.engine.FullSync(t.Context(), a.id, stream, false))
	// The queue gives up on the run while A is away.
	b.run(streamsync.FullSyncKind)
	delete(w.faults.down, a.id)
	b.run(streamsync.SweepKind)
	b.run(streamsync.FullSyncKind)
	converged(t, b, a)
}

func TestUnitSnapshotRefusals(t *testing.T) {
	w := newWorld()
	a, b, c := w.add(t), w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	put(t, a, c, fed, model.DeliveryPush)
	a.kind.Subscribe(stream, b.id, c.id)
	a.put("k", "v")
	a.put("l", "v")
	fromB, fromC := a.record(b.id), a.record(c.id)
	without := fromB
	without.UseCases = []model.UseCase{model.UseDistribution}
	stranger := model.Peer{InstanceID: "jfrt@stranger", UseCases: fed}
	page := must(a.engine.Snapshot(t.Context(), fromB, stream, "", 1))
	for _, tc := range []struct {
		name   string
		caller model.Peer
		stream string
		cursor string
		limit  int
		want   error
	}{
		{"no use case", without, stream, "", 10, streamsync.ErrRefused},
		{"unsubscribed", stranger, stream, "", 10, streamsync.ErrNoStream},
		{"no kind", fromB, "none:s", "", 10, streamsync.ErrNoStream},
		{"limit", fromB, stream, "", 0, kernel.ErrInvalid},
		{"too many", fromB, stream, "", streamsync.MaxBatch + 1, kernel.ErrInvalid},
		{"malformed", fromB, stream, "%%%", 10, kernel.ErrInvalid},
		{"not json", fromB, stream, "bm90", 10, kernel.ErrInvalid},
		{"another caller's", fromC, stream, page.NextCursor, 10, kernel.ErrInvalid},
	} {
		if _, err := a.engine.Snapshot(t.Context(), tc.caller, tc.stream, tc.cursor, tc.limit); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// A snapshot unread for longer than SnapshotIdle is pruned: its cursor
	// is gone, and the receiver takes another.
	*a.clock = a.clock.Add(streamsync.SnapshotIdle + time.Minute)
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.engine.Snapshot(t.Context(), fromB, stream, page.NextCursor, 10); !errors.Is(err, streamsync.ErrGone) {
		t.Fatalf("pruned snapshot: %v, want gone", err)
	}
	// A new snapshot replaces the caller's earlier one.
	again := must(a.engine.Snapshot(t.Context(), fromB, stream, "", 1))
	must(a.engine.Snapshot(t.Context(), fromB, stream, "", 1))
	if _, err := a.engine.Snapshot(t.Context(), fromB, stream, again.NextCursor, 10); !errors.Is(err, streamsync.ErrGone) {
		t.Fatalf("replaced snapshot: %v, want gone", err)
	}

	// A kind without a provider takes no snapshots and cannot be resynced.
	reg := a.engine.Kinds
	if err := reg.Register(plain{a.kind}); err != nil {
		t.Fatal(err)
	}
	a.kind.Subscribe("plain:s", b.id)
	if _, err := a.engine.Snapshot(t.Context(), fromB, "plain:s", "", 10); !errors.Is(err, streamsync.ErrNoSnapshot) {
		t.Fatalf("plain snapshot: %v", err)
	}
	if err := b.engine.Kinds.Register(plain{b.kind}); err != nil {
		t.Fatal(err)
	}
	b.kind.Subscribe("plain:s", a.id)
	if _, err := b.engine.FullSync(t.Context(), a.id, "plain:s", false); !errors.Is(err, streamsync.ErrNoSnapshot) {
		t.Fatalf("plain full sync: %v", err)
	}
	if _, err := b.engine.FullSync(t.Context(), "jfrt@unknown", stream, false); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("unknown peer: %v", err)
	}
	if _, err := b.engine.FullSync(t.Context(), a.id, "none:s", false); !errors.Is(err, streamsync.ErrNoStream) {
		t.Fatalf("no kind: %v", err)
	}
}

func TestUnitFullSyncStopsWhenTheTrustEnds(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	a.put("k", "v")
	run := must(b.engine.FullSync(t.Context(), a.id, stream, false))
	b.kind.Subscribe(stream)
	if err := b.engine.Resync(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
	got := must(b.store.Federation().FullSync(t.Context(), run.ID))
	if got.Status != retentionapi.RunFailed || got.Message == "" || len(b.resyncs()) != 0 {
		t.Fatalf("run %+v, flags %+v", got, b.resyncs())
	}
	// Nothing flagged and nobody to probe: nothing happens.
	if err := b.engine.Resync(t.Context(), "jfrt@unknown", stream); err != nil {
		t.Fatal(err)
	}
}

// plain is the test kind under another name, without its snapshot side.
type plain struct{ k *streamtest.Kind }

func (p plain) Name() string { return "plain" }
func (p plain) UseCases(d streamsync.Direction) []model.UseCase {
	return p.k.UseCases(d)
}
func (p plain) Streams(ctx context.Context) ([]string, error) { return p.k.Streams(ctx) }
func (p plain) Subscribers(ctx context.Context, s string) ([]string, error) {
	return p.k.Subscribers(ctx, s)
}

func (p plain) References(ctx context.Context, s, sha256 string) (bool, error) {
	return p.k.References(ctx, s, sha256)
}

func (p plain) Apply(ctx context.Context, e outbox.OutboxEntry, held bool) error {
	return p.k.Apply(ctx, e, held)
}

func (p plain) Publish(ctx context.Context, s, sha256 string) error {
	return p.k.Publish(ctx, s, sha256)
}

func (p plain) Pending(ctx context.Context, s, sha256 string) (int64, bool, error) {
	return p.k.Pending(ctx, s, sha256)
}
