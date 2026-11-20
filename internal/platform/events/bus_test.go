package events

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type dispatchStore interface {
	Store
	Settings() settings.SettingsStore
}

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time           { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *clock) advance(by time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.at = c.at.Add(by) }

type world struct {
	store dispatchStore
	clock *clock
	queue *work.Queue
	bus   *Bus
}

func newWorld(t *testing.T, st dispatchStore, c *clock, batch int) world {
	t.Helper()
	if c == nil {
		c = &clock{at: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	}
	q, err := work.New(st, work.Options{Now: c.now, Lease: time.Minute, MaxAttempts: 2, Backoff: func(int) time.Duration { return time.Second }})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(st, q, Options{Origin: "instance", Now: c.now, Batch: batch, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return world{store: st, clock: c, queue: q, bus: b}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (w world) append(t *testing.T, topic string, payload []byte) outbox.Event {
	t.Helper()
	e, err := w.store.Events().AppendEvent(t.Context(), outbox.Event{Origin: "instance", Topic: topic, Key: "same/repo/path", At: w.clock.now(), Payload: payload})
	must(t, err)
	return e
}

func (w world) run(t *testing.T, consumer string) {
	t.Helper()
	_, err := w.queue.RunDue(t.Context(), WorkKind(consumer))
	must(t, err)
}

func (w world) position(t *testing.T, consumer string) outbox.Position {
	t.Helper()
	_, p, err := w.store.Events().Next(t.Context(), "instance", consumer, 100)
	must(t, err)
	return p
}

func (w world) effect(consumer string) Handler {
	return func(ctx context.Context, delivery Delivery) error {
		var keys []string
		err := w.store.Settings().Get(ctx, "effect/"+consumer, &keys)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return err
		}
		return w.store.Settings().Put(ctx, "effect/"+consumer, append(keys, delivery.Key))
	}
}

func TestUnitDispatch(t *testing.T) {
	runDispatch(t, func(*testing.T) dispatchStore { return inmem.New(memtx.New()) })
}

func TestAccDispatch(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module}, func(db *dialect.Store) *platsql.Store { return platsql.New(db) },
		func(t *testing.T, newStore func(*testing.T) *platsql.Store) {
			runDispatch(t, func(t *testing.T) dispatchStore { return newStore(t) })
		}, sqltest.WithPostgres())
}

func runDispatch(t *testing.T, newStore func(*testing.T) dispatchStore) {
	t.Helper()
	for name, test := range map[string]func(*testing.T, world){
		"independent ordered consumers and parking":             independentConsumers,
		"restart after committed event before queue completion": restartAfterCommit,
		"expired lease rolls back effects and ack":              expiredLease,
		"maximum payload survives durable handoff and pruning":  largeHandoff,
		"shutdown returns unfinished work without an attempt":   shutdown,
		"takeover before effect rejects stale acknowledgement":  func(t *testing.T, w world) { takeover(t, w, true) },
		"takeover of empty poll cannot dirty replacement":       func(t *testing.T, w world) { takeover(t, w, false) },
	} {
		t.Run(name, func(t *testing.T) { test(t, newWorld(t, newStore(t), nil, 2)) })
	}
}

func independentConsumers(t *testing.T, w world) {
	poison := true
	must(t, w.bus.Subscribe("artifact.deployed", "bad", func(ctx context.Context, d Delivery) error {
		if err := w.effect("bad")(ctx, d); err != nil {
			return err
		}
		if poison {
			return work.ErrPermanent
		}
		return nil
	}))
	must(t, w.bus.Subscribe("artifact.deployed", "good", w.effect("good")))
	must(t, w.bus.RegisterWork(t.Context()))
	a := w.append(t, "artifact.deployed", []byte(`{"v":1}`))
	b := w.append(t, "artifact.deployed", []byte(`{"v":2}`))
	w.run(t, "bad")
	if w.position(t, "bad").Seq != 0 {
		t.Fatal("failed consumer advanced")
	}
	var effects []string
	if err := w.store.Settings().Get(t.Context(), "effect/bad", &effects); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("failed effect committed: %v", err)
	}
	parked, err := w.store.WorkQueue().Get(t.Context(), WorkKind("bad"), "instance")
	must(t, err)
	if parked.State != work.WorkParked || parked.Attempts != 1 {
		t.Fatalf("poison consumer was not parked: %+v", parked)
	}
	w.run(t, "good")
	if w.position(t, "good").Seq != 2 {
		t.Fatal("failing consumer blocked independent consumer")
	}
	must(t, w.store.Settings().Get(t.Context(), "effect/good", &effects))
	if len(effects) != 2 || effects[0] != DeliveryKey("good", a) || effects[1] != DeliveryKey("good", b) || effects[0] == effects[1] {
		t.Fatal("same-entity events were coalesced or reordered")
	}
	// Startup re-registers the same durable positions and reactivates the
	// poison consumer; already committed effects of another are not repeated.
	poison = false
	must(t, w.bus.RegisterWork(t.Context()))
	w.run(t, "bad")
	w.run(t, "good")
	must(t, w.store.Settings().Get(t.Context(), "effect/good", &effects))
	if len(effects) != 2 || w.position(t, "bad").Seq != 2 {
		t.Fatal("recovery lost or repeated a committed event")
	}
	next, _, err := w.store.Events().Next(t.Context(), "instance", "good", 100)
	must(t, err)
	if len(next) != 0 {
		t.Fatal("fully acknowledged prefix was retained unexpectedly")
	}
}

func restartAfterCommit(t *testing.T, w world) {
	must(t, w.bus.Subscribe("artifact.deployed", "proof", w.effect("proof")))
	must(t, w.bus.RegisterWork(t.Context()))
	w.append(t, "ignored", nil)
	w.append(t, "artifact.deployed", nil)
	w.append(t, "artifact.deployed", nil)
	items, err := w.store.WorkQueue().
		Lease(t.Context(), work.WorkLease{Kinds: []string{WorkKind("proof")}, Holder: "crashed", Limit: 1, Now: w.clock.now(), Until: w.clock.now().Add(time.Minute)})
	must(t, err)
	if len(items) != 1 {
		t.Fatal("dispatch was not leased")
	}
	must(t, w.bus.dispatch(t.Context(), "proof", items[0]))
	if w.position(t, "proof").Seq != 2 {
		t.Fatal("bounded first run failed to acknowledge ignored and matching topics")
	}
	// Do not settle the queue run: the process crashed after committing Ack.
	w.clock.advance(2 * time.Minute)
	fresh := newWorld(t, w.store, w.clock, 2)
	must(t, fresh.bus.Subscribe("artifact.deployed", "proof", fresh.effect("proof")))
	must(t, fresh.bus.RegisterWork(t.Context()))
	fresh.run(t, "proof")
	var effects []string
	must(t, w.store.Settings().Get(t.Context(), "effect/proof", &effects))
	if len(effects) != 2 || fresh.position(t, "proof").Seq != 3 {
		t.Fatalf("restart duplicated or lost effects: %v", effects)
	}
	item, err := w.store.WorkQueue().Get(t.Context(), WorkKind("proof"), "instance")
	must(t, err)
	if item.State != work.WorkPending || item.Generation <= items[0].Generation {
		t.Fatal("successful polling lost its durable generation")
	}
}

func expiredLease(t *testing.T, w world) {
	must(t, w.bus.Subscribe("artifact.deployed", "proof", func(ctx context.Context, d Delivery) error {
		if err := w.effect("proof")(ctx, d); err != nil {
			return err
		}
		w.clock.advance(2 * time.Minute)
		return nil
	}))
	must(t, w.bus.RegisterWork(t.Context()))
	w.append(t, "artifact.deployed", nil)
	w.run(t, "proof")
	if w.position(t, "proof").Seq != 0 {
		t.Fatal("expired dispatcher acknowledged")
	}
	var effects []string
	if err := w.store.Settings().Get(t.Context(), "effect/proof", &effects); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("expired dispatcher committed effect: %v", err)
	}
}

func largeHandoff(t *testing.T, w world) {
	var key string
	payload := []byte(`{"data":"` + strings.Repeat("x", outbox.MaxEventPayload-len(`{"data":""}`)) + `"}`)
	if len(payload) != outbox.MaxEventPayload {
		t.Fatal("not exercising maximum event payload")
	}
	must(t, w.bus.Subscribe("artifact.deployed", "delivery", func(ctx context.Context, d Delivery) error {
		key = d.Key
		if err := w.store.Settings().Put(ctx, "handoff/"+d.Key, string(d.Event.Payload)); err != nil {
			return err
		}
		return w.queue.Enqueue(ctx, "proof.delivery", d.Key, nil)
	}))
	must(t, w.bus.RegisterWork(t.Context()))
	w.append(t, "artifact.deployed", payload)
	w.run(t, "delivery")
	if key == "" {
		t.Fatal("no handoff")
	}
	var retained string
	must(t, w.store.Settings().Get(t.Context(), "handoff/"+key, &retained))
	if retained != string(payload) {
		t.Fatal("handoff lost or truncated the full payload")
	}
	_, err := w.store.WorkQueue().Get(t.Context(), "proof.delivery", key)
	must(t, err)
	if count, err := w.store.Events().PruneAcknowledged(t.Context(), "instance", 1); err != nil || count != 0 {
		t.Fatalf("event was not already pruned after the durable handoff: %d %v", count, err)
	}
}

func shutdown(t *testing.T, w world) {
	entered := make(chan struct{})
	must(t, w.bus.Subscribe("artifact.deployed", "shutdown", func(ctx context.Context, d Delivery) error {
		if err := w.effect("shutdown")(ctx, d); err != nil {
			return err
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	must(t, w.bus.RegisterWork(t.Context()))
	w.append(t, "artifact.deployed", nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.queue.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer never ran")
	}
	cancel()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("queue did not finish shutdown")
	}
	if w.position(t, "shutdown").Seq != 0 {
		t.Fatal("shutdown acknowledged unfinished event")
	}
	item, err := w.store.WorkQueue().Get(t.Context(), WorkKind("shutdown"), "instance")
	must(t, err)
	if item.State != work.WorkPending || item.Attempts != 0 {
		t.Fatalf("shutdown counted a fake failure: %+v", item)
	}
	var effects []string
	if err := w.store.Settings().Get(t.Context(), "effect/shutdown", &effects); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("shutdown committed unfinished effect")
	}
}
