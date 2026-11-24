package sync_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func (k faultKind) Paused(_ context.Context, _, peer string) (bool, error) {
	if err := k.f.check("Kind.Paused"); err != nil {
		return false, err
	}
	k.f.mu.Lock()
	defer k.f.mu.Unlock()
	return k.f.paused[peer], nil
}

func (f *faultSet) pause(peer string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paused == nil {
		f.paused = map[string]bool{}
	}
	f.paused[peer] = on
}

// TestUnitPausedPeerResumesFromItsPosition pauses pushing to a subscribed
// peer and its pulls, then resumes both from the acknowledged position:
// a federated repository's disabled member (decision 0022).
func TestUnitPausedPeerResumesFromItsPosition(t *testing.T) {
	w, a, b := pushed(t)
	a.put("k", "1")
	a.run(streamsync.SendKind)
	w.inject.pause(b.id, true)
	a.put("k", "2")
	a.put("k", "3")
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 1))
	// A paused peer's pull gets no entries, and its acknowledgement holds.
	page, err := a.engine.Events(t.Context(), a.record(b.id), stream, 1, 10)
	if err != nil || len(page.Entries) != 0 || page.Origin != a.id {
		t.Fatalf("paused pull = %+v, %v", page, err)
	}
	if a.cursor(b) != 1 {
		t.Fatalf("cursor %d, want 1", a.cursor(b))
	}
	// Ending the pause and waking the stream sends what was held back,
	// without waiting for the sweep.
	w.inject.pause(b.id, false)
	if err := a.engine.Wake(t.Context(), stream); err != nil {
		t.Fatal(err)
	}
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 3))
	if err := a.engine.Wake(t.Context(), "other:x"); err != nil {
		t.Fatalf("waking a stream of no kind: %v", err)
	}

	// A kind failing to say fails the dispatch and the pull.
	w.inject.arm("Kind.Paused", 0)
	if err := a.engine.Dispatch(t.Context(), b.id, stream); !errors.Is(err, errBoom) {
		t.Fatalf("dispatch = %v", err)
	}
	if _, err := a.engine.Events(t.Context(), a.record(b.id), stream, 3, 10); !errors.Is(err, errBoom) {
		t.Fatalf("events = %v", err)
	}
}

// TestUnitObserveOrdersLaterAppends: a time observed outside an apply
// still orders every later local change after it.
func TestUnitObserveOrdersLaterAppends(t *testing.T) {
	w := newWorld()
	a := w.add(t)
	ahead := outbox.HLC{Wall: t0.UnixMilli() + 3_600_000, Logical: 9}
	a.engine.Observe(ahead)
	en := a.put("k", "v")
	if !(model.Stamp{Time: en.Time}).After(model.Stamp{Time: ahead}) {
		t.Fatalf("appended at %v, not after the observed %v", en.Time, ahead)
	}
}
