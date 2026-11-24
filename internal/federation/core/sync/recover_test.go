package sync_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// A delivery backing off after a failure is retried at once by recovery,
// from the acknowledged cursor, so nothing applies twice; a plain recovery
// leaves a healthy peer alone, and a forced one sends to it too.
func TestUnitRecoverRetriesFailedDelivery(t *testing.T) {
	w, a, b := pushed(t)
	a.put("k", "1")
	a.run(streamsync.SendKind)
	w.faults.down[b.id] = true
	a.put("k", "2")
	if err := a.engine.Dispatch(t.Context(), b.id, stream); err == nil {
		t.Fatal("dispatch to a peer that is down succeeded")
	}
	if c := must(a.store.Outbox().Cursor(t.Context(), b.id, stream)); c.LastError == "" || c.AckedSeq != 1 {
		t.Fatalf("cursor %+v, want 1 with the failure", c)
	}
	delete(w.faults.down, b.id)

	if err := a.engine.QueueRecovery(t.Context(), stream, false); err != nil {
		t.Fatal(err)
	}
	a.run(streamsync.RecoverKind)
	wantApplied(t, b.applied(a), seqs(1, 2))
	if c := must(a.store.Outbox().Cursor(t.Context(), b.id, stream)); c.LastError != "" || c.AckedSeq != 2 {
		t.Fatalf("cursor %+v, want 2 and no failure", c)
	}

	// Nothing failed since: a plain recovery sends nothing, a forced one
	// sends what is waiting.
	pushes := w.pushes
	a.put("k", "3")
	if err := a.engine.Recover(t.Context(), stream, false); err != nil {
		t.Fatal(err)
	}
	if w.pushes != pushes {
		t.Fatalf("a plain recovery pushed %d times to a healthy peer", w.pushes-pushes)
	}
	if err := a.engine.Recover(t.Context(), stream, true); err != nil {
		t.Fatal(err)
	}
	wantApplied(t, b.applied(a), seqs(1, 3))

	// A stream of no kind here has nothing to recover.
	if err := a.engine.Recover(t.Context(), "none:x", true); err != nil {
		t.Fatal(err)
	}
}

// A forced recovery pulls from a peer this instance pulls from.
func TestUnitRecoverForcedPulls(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	// B pulls from A; A records B as pulled.
	pair(t, a, b, fed, model.DeliveryPulled, model.DeliveryPull)
	a.put("k", "1")
	if err := b.engine.Recover(t.Context(), stream, true); err != nil {
		t.Fatal(err)
	}
	wantApplied(t, b.applied(a), seqs(1, 1))
}

// A forced recovery is queued apart from a plain one, so the plain one
// cannot take its force away.
func TestUnitQueueRecoveryKeepsForce(t *testing.T) {
	_, a, _ := pushed(t)
	for _, force := range []bool{true, false} {
		if err := a.engine.QueueRecovery(t.Context(), stream, force); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{stream, "force " + stream} {
		if _, err := a.store.WorkQueue().Get(t.Context(), streamsync.RecoverKind, key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	a.run(streamsync.RecoverKind)
}

// Forgetting a peer drops its cursor and stops the full sync between the
// two, so nothing holds the stream back for it.
func TestUnitForgetPeer(t *testing.T) {
	_, a, b := pushed(t)
	a.put("k", "1")
	a.run(streamsync.SendKind)
	run, err := b.engine.FullSync(t.Context(), a.id, stream, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.engine.Forget(t.Context(), b.id, stream); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Outbox().Cursor(t.Context(), b.id, stream); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("cursor after forget: %v, want none", err)
	}
	if err := b.engine.Forget(t.Context(), a.id, stream); err != nil {
		t.Fatal(err)
	}
	if got := b.resyncs(); len(got) != 0 {
		t.Fatalf("flags after forget: %+v, want none", got)
	}
	r := must(b.store.Federation().FullSync(t.Context(), run.ID))
	if r.Status != retentionapi.RunFailed || r.Message == "" {
		t.Fatalf("run after forget: %+v, want stopped with why", r)
	}
	// Forgetting again, or a peer never known, changes nothing.
	if err := a.engine.Forget(t.Context(), b.id, stream); err != nil {
		t.Fatal(err)
	}
}

// A stream of higher priority is queued ahead of the others, so under
// contention its dispatcher runs first.
func TestUnitPriorityDispatchesFirst(t *testing.T) {
	const high = "test:high"
	w, a, b := pushed(t)
	a.kind.Subscribe(high, b.id)
	b.kind.Subscribe(high, a.id)
	a.engine.Priority = func(_ context.Context, s string) int {
		if s == high {
			return 1
		}
		return 0
	}
	a.put("k", "standard")
	if _, err := a.kind.Commit(t.Context(), a.engine, high, streamtest.Change{
		Type: streamtest.Put, Key: "k", Value: "high",
	}, nil); err != nil {
		t.Fatal(err)
	}
	std := must(a.store.WorkQueue().Get(t.Context(), streamsync.SendKind, b.id+" "+stream))
	hi := must(a.store.WorkQueue().Get(t.Context(), streamsync.SendKind, b.id+" "+high))
	if lead := std.NotBefore.Sub(hi.NotBefore); lead != streamsync.PriorityLead {
		t.Fatalf("high priority leads by %v, want %v", lead, streamsync.PriorityLead)
	}
	a.run(streamsync.SendKind)
	if i, j := slices.Index(w.pushed, high), slices.Index(w.pushed, stream); i < 0 || j < 0 || i > j {
		t.Fatalf("pushed %v, want %s first", w.pushed, high)
	}
}

// Pruning runs as often as PruneInterval, or a sixth of a shorter
// retention window.
func TestUnitPruneEvery(t *testing.T) {
	for _, c := range []struct {
		retention, want time.Duration
	}{
		{0, streamsync.PruneInterval},
		{time.Minute, 10 * time.Second},
		{time.Second, time.Second},
		{30 * 24 * time.Hour, streamsync.PruneInterval},
	} {
		e := streamsync.Engine{Retention: c.retention}
		if got := e.PruneEvery(); got != c.want {
			t.Errorf("retention %v: every %v, want %v", c.retention, got, c.want)
		}
	}
}

// A store failure in recovery's or forgetting's own reads and writes is
// reported; one in a delivery recovery runs is left to the queue's
// back-off, as any run's is. Once the failure clears, recovery delivers.
func TestUnitRecoverAndForgetFailuresSurface(t *testing.T) {
	for _, name := range []string{
		"Instance.ID", "Peers.Get", "Kind.Subscribers", "Outbox.Cursor", "Outbox.PutCursor",
		"Resyncs.Clear", "Outbox.DeleteCursor",
	} {
		for skip := range 3 {
			t.Run(fmt.Sprintf("%s-%d", name, skip), func(t *testing.T) {
				w, a, b := pushed(t)
				w.faults.down[b.id] = true
				a.put("k", "1")
				_ = a.engine.Dispatch(t.Context(), b.id, stream)
				delete(w.faults.down, b.id)
				if _, err := b.engine.FullSync(t.Context(), a.id, stream, false); err != nil {
					t.Fatal(err)
				}
				w.inject.arm(name, skip)
				errs := []error{
					a.engine.Recover(t.Context(), stream, true),
					b.engine.Forget(t.Context(), a.id, stream),
					a.engine.Forget(t.Context(), b.id, stream),
				}
				own := skip == 0 && name != "Outbox.PutCursor"
				if own && w.inject.hit() > 0 && !slices.ContainsFunc(errs, func(err error) bool { return errors.Is(err, errBoom) }) {
					t.Fatalf("%s failed %d calls and nothing reported it: %v", name, w.inject.hit(), errs)
				}
				w.inject.arm("", 0)
				if err := b.engine.Forget(t.Context(), a.id, stream); err != nil {
					t.Fatal(err)
				}
				if err := a.engine.Recover(t.Context(), stream, true); err != nil {
					t.Fatal(err)
				}
				wantApplied(t, b.applied(a), seqs(1, 1))
			})
		}
	}
}
