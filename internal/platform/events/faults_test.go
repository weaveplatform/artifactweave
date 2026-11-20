package events

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitSubscriptionValidationAndFrozenRegistration(t *testing.T) {
	st := inmem.New(memtx.New())
	queue, err := work.New(st, work.Options{})
	must(t, err)
	for _, options := range []Options{{}, {Origin: "instance", Batch: -1}, {Origin: "instance", Batch: kernel.MaxLimit + 1}, {Origin: "instance", Poll: -time.Second}} {
		if _, err := New(st, queue, options); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("invalid bus accepted: %+v %v", options, err)
		}
	}
	if _, err := New(nil, queue, Options{Origin: "instance"}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("nil store accepted")
	}
	if _, err := New(st, nil, Options{Origin: "instance"}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("nil queue accepted")
	}
	bus, err := New(st, queue, Options{Origin: "instance"})
	must(t, err)
	handler := func(context.Context, Delivery) error { return nil }
	for _, bad := range []struct {
		topic, consumer string
		handler         Handler
	}{
		{"", "proof", handler},
		{"topic", "", handler},
		{"topic", strings.Repeat("x", work.MaxWorkKind), handler},
		{strings.Repeat("x", 129), "proof", handler},
		{"a\x00b", "proof", handler},
		{"topic", "proof", nil},
	} {
		if err := bus.Subscribe(bad.topic, bad.consumer, bad.handler); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("invalid subscription accepted: %v", err)
		}
	}
	must(t, bus.Subscribe("one", "proof", handler))
	must(t, bus.Subscribe("two", "proof", handler))
	if err := bus.Subscribe("one", "proof", handler); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("duplicate handler: %v", err)
	}
	if err := bus.dispatch(t.Context(), "proof", work.WorkItem{Kind: WorkKind("proof"), Key: "instance"}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unregistered consumer ran")
	}
	must(t, bus.RegisterWork(t.Context()))
	if err := bus.Subscribe("three", "proof", handler); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("late subscription changed an active consumer")
	}
	if err := bus.dispatch(t.Context(), "proof", work.WorkItem{Kind: WorkKind("proof"), Key: "foreign"}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("foreign-origin dispatch ran")
	}
	if DeliveryKey("a:b", outbox.Event{Origin: "c", Seq: 1}) == DeliveryKey("a", outbox.Event{Origin: "b:c", Seq: 1}) {
		t.Fatal("delivery identity has a delimiter collision")
	}
	if DeliveryKey("a", outbox.Event{Origin: "c", Seq: 1}) == DeliveryKey("a", outbox.Event{Origin: "c", Seq: 2}) {
		t.Fatal("two events share a delivery identity")
	}
}

var errStorage = errors.New("injected event storage failure")

type faultStore struct {
	dispatchStore
	op string
}

func (s *faultStore) Events() outbox.EventStore {
	return faultEvents{EventStore: s.dispatchStore.Events(), fault: s}
}

func (s *faultStore) WorkQueue() work.WorkQueue {
	return faultQueue{WorkQueue: s.dispatchStore.WorkQueue(), fault: s}
}

type faultEvents struct {
	outbox.EventStore
	fault *faultStore
}

func (s faultEvents) RegisterConsumer(ctx context.Context, origin, consumer string) (outbox.Position, error) {
	if s.fault.op == "register" {
		return outbox.Position{}, errStorage
	}
	return s.EventStore.RegisterConsumer(ctx, origin, consumer)
}

func (s faultEvents) Next(ctx context.Context, origin, consumer string, limit int) ([]outbox.Event, outbox.Position, error) {
	if s.fault.op == "next" {
		return nil, outbox.Position{}, errStorage
	}
	return s.EventStore.Next(ctx, origin, consumer, limit)
}

func (s faultEvents) Ack(ctx context.Context, position outbox.Position, seq int64) error {
	if s.fault.op == "ack" {
		return errStorage
	}
	return s.EventStore.Ack(ctx, position, seq)
}

func (s faultEvents) PruneAcknowledged(ctx context.Context, origin string, through int64) (int64, error) {
	if s.fault.op == "prune" {
		return 0, errStorage
	}
	return s.EventStore.PruneAcknowledged(ctx, origin, through)
}

type faultQueue struct {
	work.WorkQueue
	fault *faultStore
}

func (s faultQueue) Enqueue(ctx context.Context, kind, key string, payload []byte, now time.Time) error {
	if s.fault.op == "enqueue" {
		return errStorage
	}
	return s.WorkQueue.Enqueue(ctx, kind, key, payload, now)
}

func (s faultQueue) GuardLease(ctx context.Context, kind, key string, gen int64) (time.Time, error) {
	if s.fault.op == "guard" {
		return time.Time{}, errStorage
	}
	return s.WorkQueue.GuardLease(ctx, kind, key, gen)
}

func TestUnitRegistrationFailureCanRetryWithoutPartialConsumers(t *testing.T) {
	for _, op := range []string{"register", "enqueue"} {
		t.Run(op, func(t *testing.T) {
			st := &faultStore{dispatchStore: inmem.New(memtx.New()), op: op}
			w := newWorld(t, st, nil, 1)
			must(t, w.bus.Subscribe("topic", "proof", w.effect("proof")))
			if err := w.bus.RegisterWork(t.Context()); !errors.Is(err, errStorage) {
				t.Fatalf("registration fault: %v", err)
			}
			if _, _, err := st.dispatchStore.Events().Next(t.Context(), "instance", "proof", 1); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("partial consumer registration committed")
			}
			st.op = ""
			must(t, w.bus.RegisterWork(t.Context()))
			w.append(t, "topic", nil)
			w.run(t, "proof")
			if w.position(t, "proof").Seq != 1 {
				t.Fatal("registration could not recover")
			}
		})
	}
}

func TestUnitStorageFailuresDoNotLoseOrRepeatCommittedEffects(t *testing.T) {
	for _, op := range []string{"next", "ack", "guard", "prune", "enqueue"} {
		t.Run(op, func(t *testing.T) {
			st := &faultStore{dispatchStore: inmem.New(memtx.New())}
			w := newWorld(t, st, nil, 1)
			must(t, w.bus.Subscribe("topic", "proof", w.effect("proof")))
			must(t, w.bus.RegisterWork(t.Context()))
			w.append(t, "topic", nil)
			st.op = op
			w.run(t, "proof")
			st.op = ""
			wantCommitted := op == "prune" || op == "enqueue"
			if (w.position(t, "proof").Seq == 1) != wantCommitted {
				t.Fatalf("%s committed wrong cursor", op)
			}
			var effects []string
			err := st.Settings().Get(t.Context(), "effect/proof", &effects)
			if wantCommitted {
				must(t, err)
				if len(effects) != 1 {
					t.Fatal("committed effect missing")
				}
			} else if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("failed event retained a partial effect")
			}
			item, err := st.WorkQueue().Get(t.Context(), WorkKind("proof"), "instance")
			must(t, err)
			if item.State != work.WorkPending || item.Attempts != 1 || item.LastError == "" {
				t.Fatalf("storage fault did not retain retryable work: %+v", item)
			}
			w.clock.advance(time.Second)
			w.run(t, "proof")
			must(t, st.Settings().Get(t.Context(), "effect/proof", &effects))
			if len(effects) != 1 || w.position(t, "proof").Seq != 1 {
				t.Fatal("storage retry lost or repeated a committed effect")
			}
		})
	}
}
