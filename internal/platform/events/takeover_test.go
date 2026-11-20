package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type gatedNextStore struct {
	dispatchStore
	once             sync.Once
	entered, release chan struct{}
}

func (s *gatedNextStore) Events() outbox.EventStore {
	return gatedNextEvents{EventStore: s.dispatchStore.Events(), gate: s}
}

type gatedNextEvents struct {
	outbox.EventStore
	gate *gatedNextStore
}

func (s gatedNextEvents) Next(ctx context.Context, origin, consumer string, limit int) ([]outbox.Event, outbox.Position, error) {
	batch, position, err := s.EventStore.Next(ctx, origin, consumer, limit)
	if err != nil {
		return nil, outbox.Position{}, err
	}
	s.gate.once.Do(func() {
		close(s.gate.entered)
		select {
		case <-s.gate.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return batch, position, err
}

// Stop after the unlocked read, expire the lease and let an actual second
// generation take over before the old dispatcher begins its effect Tx.
func takeover(t *testing.T, w world, event bool) {
	gate := &gatedNextStore{dispatchStore: w.store, entered: make(chan struct{}), release: make(chan struct{})}
	w = newWorld(t, gate, w.clock, 2)
	must(t, w.bus.Subscribe("topic", "proof", w.effect("proof")))
	must(t, w.bus.RegisterWork(t.Context()))
	if event {
		w.append(t, "topic", nil)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lease := func(holder string) work.WorkItem {
		item, took, err := w.store.WorkQueue().
			LeaseKey(ctx, WorkKind("proof"), "instance", work.WorkLease{Holder: holder, Now: w.clock.now(), Until: w.clock.now().Add(time.Minute)})
		must(t, err)
		if !took {
			t.Fatal("dispatcher failed to take lease")
		}
		return item
	}
	old := lease("old")
	finished := make(chan error, 1)
	go func() { finished <- w.bus.dispatch(ctx, "proof", old) }()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("dispatcher did not reach read barrier")
	}
	w.clock.advance(2 * time.Minute)
	replacement := lease("replacement")
	close(gate.release)
	select {
	case err := <-finished:
		if !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("stale dispatcher was not refused: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stale dispatcher did not finish")
	}
	if w.position(t, "proof").Seq != 0 {
		t.Fatal("stale dispatcher advanced position")
	}
	var effects []string
	if err := w.store.Settings().Get(ctx, "effect/proof", &effects); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("stale dispatcher committed effect")
	}
	current, err := w.store.WorkQueue().Get(ctx, WorkKind("proof"), "instance")
	must(t, err)
	if current.Generation != replacement.Generation || !current.DirtySince.IsZero() {
		t.Fatal("stale dispatcher changed replacement polling state")
	}
	must(t, w.bus.dispatch(ctx, "proof", replacement))
	if event {
		must(t, w.store.Settings().Get(ctx, "effect/proof", &effects))
		if len(effects) != 1 || w.position(t, "proof").Seq != 1 {
			t.Fatal("replacement lost or repeated event")
		}
	}
}
