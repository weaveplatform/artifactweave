package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// EventContractStore is the shared unit of work and both views of its outbox.
type EventContractStore interface {
	tx.Runner
	Events() outbox.EventStore
	Outbox() outbox.OutboxStore
}

// RunEvents tests transactional delivery positions, pruning and peer coexistence
// against the same contract on reference and SQL stores.
func RunEvents(t *testing.T, newStore func(*testing.T) EventContractStore) {
	t.Helper()
	t.Run("EventsAndAcknowledgements", func(t *testing.T) { eventAcknowledgements(t, newStore(t)) })
	t.Run("EventPruning", func(t *testing.T) { eventPruning(t, newStore(t)) })
	t.Run("EventRollback", func(t *testing.T) { eventRollback(t, newStore(t)) })
	t.Run("ConcurrentEventAppends", func(t *testing.T) { concurrentEvents(t, newStore(t)) })
	t.Run("ConcurrentEventAcknowledgements", func(t *testing.T) { concurrentEventAcks(t, newStore(t)) })
	t.Run("ConcurrentEventRegistrationAndPruning", func(t *testing.T) { concurrentEventPruning(t, newStore(t)) })
	t.Run("EventValidationAndIsolation", func(t *testing.T) { eventValidation(t, newStore(t)) })
}

func businessEvent(n int) outbox.Event {
	return outbox.Event{
		Origin: "instance", Topic: "artifact.deployed", Key: "libs/a.jar",
		At: time.Date(2026, 11, 19, 12, 0, 0, n*1000, time.UTC), Payload: fmt.Appendf(nil, `{"n":%d}`, n),
	}
}

func eventAcknowledgements(t *testing.T, s EventContractStore) {
	ctx := t.Context()
	e := s.Events()
	a := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "a"))
	storekit.Same(t, "idempotent registration", a, storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "a")))
	if _, _, err := e.Next(ctx, "instance", "unknown", 0); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("unknown consumer: %v", err)
	}
	for n := range 2 {
		got := storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, businessEvent(n)))
		if got.Seq != int64(n+1) {
			t.Fatalf("allocated identity: %+v", got)
		}
	}
	batch, pos, err := e.Next(ctx, "instance", "a", 1)
	storekit.Must(t, err)
	if len(batch) != 1 || batch[0].Seq != 1 || pos != a {
		t.Fatalf("first bounded read: %+v at %+v", batch, pos)
	}
	batch[0].Payload[0] = 'X'
	again, _, err := e.Next(ctx, "instance", "a", 1)
	storekit.Must(t, err)
	if len(again) != 1 || string(again[0].Payload) != `{"n":0}` {
		t.Fatalf("event read leaked mutable storage: %+v", again)
	}
	if err := e.Ack(ctx, a, 2); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("skipped acknowledgement: %v", err)
	}
	storekit.Must(t, e.Ack(ctx, a, 1))
	if err := e.Ack(ctx, a, 1); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale acknowledgement: %v", err)
	}
	a.Seq = 1
	storekit.Must(t, e.Ack(ctx, a, 2))
	a.Seq = 2
	if err := e.Ack(ctx, a, 3); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("acknowledged missing event: %v", err)
	}
	batch, pos, err = e.Next(ctx, "instance", "a", 0)
	storekit.Must(t, err)
	if len(batch) != 0 || pos.Seq != 2 {
		t.Fatalf("read after acknowledgement: %+v at %+v", batch, pos)
	}
}

func eventPruning(t *testing.T, s EventContractStore) {
	ctx := t.Context()
	e := s.Events()
	for n := range 3 {
		storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, businessEvent(n)))
	}
	removed := storekit.Get[int64](t)(s.Outbox().Prune(ctx, "instance", outbox.ModuleEventStream, 100))
	if removed != 0 {
		t.Fatal("raw prune bypassed no-consumer retention")
	}
	a := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "a"))
	b := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "b"))
	storekit.Must(t, e.Ack(ctx, a, 1))
	a.Seq = 1
	storekit.Must(t, e.Ack(ctx, a, 2))
	if n := storekit.Get[int64](t)(e.PruneAcknowledged(ctx, "instance", 100)); n != 0 {
		t.Fatal("pruned an event before slow consumer acknowledged it")
	}
	storekit.Must(t, e.Ack(ctx, b, 1))
	if n := storekit.Get[int64](t)(s.Outbox().Prune(ctx, "instance", outbox.ModuleEventStream, 100)); n != 1 {
		t.Fatalf("raw prune must obey minimum acknowledgement, removed %d", n)
	}
	late := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "late"))
	if late.Seq != 1 {
		t.Fatalf("late consumer promised pruned history: %+v", late)
	}
	batch, pos, err := e.Next(ctx, "instance", "late", 0)
	storekit.Must(t, err)
	if len(batch) != 2 || batch[0].Seq != 2 || pos.Seq != 1 {
		t.Fatalf("late consumer retained prefix: %+v at %+v", batch, pos)
	}
	b.Seq = 1
	storekit.Must(t, e.Ack(ctx, b, 2))
	storekit.Must(t, e.Ack(ctx, late, 2))
	if n := storekit.Get[int64](t)(e.PruneAcknowledged(ctx, "instance", 100)); n != 1 {
		t.Fatalf("second minimum-ack prune: %d", n)
	}
	if got := storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, businessEvent(4))); got.Seq != 4 {
		t.Fatalf("pruning reset sequence allocation: %+v", got)
	}
	peer := entry("instance", "repo:libs", 1)
	storekit.Get[outbox.OutboxEntry](t)(s.Outbox().Append(ctx, peer))
	if n := storekit.Get[int64](t)(s.Outbox().Prune(ctx, "instance", "repo:libs", 100)); n != 1 {
		t.Fatalf("legacy peer pruning changed: %d", n)
	}
}

func eventRollback(t *testing.T, s EventContractStore) {
	ctx := t.Context()
	e := s.Events()
	rollback := kernel.ErrInvalid
	err := s.Tx(ctx, func(ctx context.Context) error {
		if _, err := e.AppendEvent(ctx, businessEvent(0)); err != nil {
			return fmt.Errorf("event contract transaction: %w", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("business rollback: %v", err)
	}
	if seq := storekit.Get[int64](t)(s.Outbox().LastSeq(ctx, "instance", outbox.ModuleEventStream)); seq != 0 {
		t.Fatalf("rolled-back append kept its sequence: %d", seq)
	}
	a := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "a"))
	storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, businessEvent(1)))
	err = s.Tx(ctx, func(ctx context.Context) error {
		if err := e.Ack(ctx, a, 1); err != nil {
			return fmt.Errorf("event contract transaction: %w", err)
		}
		if _, err := e.PruneAcknowledged(ctx, "instance", 1); err != nil {
			return fmt.Errorf("event contract transaction: %w", err)
		}
		if _, err := e.RegisterConsumer(ctx, "instance", "rolled-back"); err != nil {
			return fmt.Errorf("event contract transaction: %w", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("consumer transaction rollback: %v", err)
	}
	if _, _, err := e.Next(ctx, "instance", "rolled-back", 1); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("registration survived rollback: %v", err)
	}
	batch, pos, err := e.Next(ctx, "instance", "a", 1)
	storekit.Must(t, err)
	if len(batch) != 1 || pos != a {
		t.Fatalf("acknowledgement/pruning survived rollback: %+v at %+v", batch, pos)
	}
	storekit.Must(t, e.Ack(ctx, a, 1))
	err = s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Outbox().Append(ctx, entry("instance", "consumer-effect", 1)); err != nil {
			return fmt.Errorf("event contract transaction: %w", err)
		}
		return e.Ack(ctx, a, 1)
	})
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale consumer effect: %v", err)
	}
	if seq := storekit.Get[int64](t)(s.Outbox().LastSeq(ctx, "instance", "consumer-effect")); seq != 0 {
		t.Fatalf("stale acknowledgement committed consumer effect: %d", seq)
	}
}

func concurrentEventAcks(t *testing.T, s EventContractStore) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	e := s.Events()
	pos := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "worker"))
	storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, businessEvent(0)))
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- s.Tx(ctx, func(ctx context.Context) error {
				// A domain write before Ack models a transactional consumer effect.
				if _, err := s.Outbox().Append(ctx, entry("instance", "effect", 1)); err != nil {
					return fmt.Errorf("event contract transaction: %w", err)
				}
				return e.Ack(ctx, pos, 1)
			})
		}()
	}
	close(start)
	commits, conflicts := 0, 0
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			commits++
		case errors.Is(err, kernel.ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent acknowledgement: %v", err)
		}
	}
	if commits != 1 || conflicts != 1 {
		t.Fatalf("commits=%d conflicts=%d", commits, conflicts)
	}
	if seq := storekit.Get[int64](t)(s.Outbox().LastSeq(ctx, "instance", "effect")); seq != 1 {
		t.Fatalf("duplicate consumer committed its effect: %d", seq)
	}
}

func concurrentEventPruning(t *testing.T, s EventContractStore) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	e := s.Events()
	pos := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "original"))
	for n := range 2 {
		storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, businessEvent(n)))
	}
	storekit.Must(t, e.Ack(ctx, pos, 1))
	type result struct {
		position outbox.Position
		err      error
	}
	registered := make(chan result, 1)
	pruned := make(chan error, 1)
	go func() {
		p, err := e.RegisterConsumer(ctx, "instance", "late")
		registered <- result{p, err}
	}()
	go func() { _, err := e.PruneAcknowledged(ctx, "instance", 100); pruned <- err }()
	r := <-registered
	storekit.Must(t, r.err)
	storekit.Must(t, <-pruned)
	batch, p, err := e.Next(ctx, "instance", "late", 0)
	storekit.Must(t, err)
	if p != r.position || (p.Seq != 0 && p.Seq != 1) || len(batch) != 2-int(p.Seq) || batch[0].Seq != p.Seq+1 {
		t.Fatalf("registration/pruning lost retained events: %+v at %+v (registered %+v)", batch, p, r.position)
	}
	if n := storekit.Get[int64](t)(e.PruneAcknowledged(ctx, "instance", 100)); n != 0 {
		t.Fatalf("pruned the newly registered consumer's events: %d", n)
	}
}

func eventValidation(t *testing.T, s EventContractStore) {
	ctx := t.Context()
	e := s.Events()
	for _, bad := range []string{"", strings.Repeat("x", 257), string([]byte{0xff}), "has\x00nul"} {
		if _, err := e.RegisterConsumer(ctx, "instance", bad); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("invalid consumer registration: %v", err)
		}
		if _, _, err := e.Next(ctx, bad, "reader", 1); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("invalid origin read: %v", err)
		}
		if err := e.Ack(ctx, outbox.Position{Origin: "instance", Consumer: bad}, 1); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("invalid consumer acknowledgement: %v", err)
		}
		if _, err := e.PruneAcknowledged(ctx, bad, 1); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("invalid origin prune: %v", err)
		}
	}
	if _, err := e.PruneAcknowledged(ctx, "instance", -1); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("negative prune position: %v", err)
	}
	if err := e.Ack(ctx, outbox.Position{Origin: "instance", Consumer: "missing"}, 1); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("unknown acknowledgement consumer: %v", err)
	}
	bad := businessEvent(0)
	bad.Payload = []byte("invalid")
	if _, err := e.AppendEvent(ctx, bad); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("invalid append: %v", err)
	}
	bad = businessEvent(0)
	bad.At = time.Time{}.Add(time.Nanosecond)
	if _, err := e.AppendEvent(ctx, bad); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("timestamp lost at persisted precision: %v", err)
	}
	if n := storekit.Get[int64](t)(s.Outbox().LastSeq(ctx, "instance", outbox.ModuleEventStream)); n != 0 {
		t.Fatalf("failed event decoding committed an allocated sequence: %d", n)
	}
	for _, origin := range []string{"instance", "other"} {
		p := storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, origin, "reader"))
		if err := e.Ack(ctx, outbox.Position{Origin: origin, Consumer: "reader", Seq: -1}, 0); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("negative acknowledgement: %v", err)
		}
		for n := range kernel.DefaultLimit + 1 {
			event := businessEvent(n)
			event.Origin = origin
			storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, event))
		}
		batch, got, err := e.Next(ctx, origin, "reader", -1)
		storekit.Must(t, err)
		if len(batch) != kernel.DefaultLimit || got != p || batch[0].Origin != origin || batch[0].Seq != 1 {
			t.Fatalf("default bound or origin isolation: count=%d position=%+v", len(batch), got)
		}
		batch, _, err = e.Next(ctx, origin, "reader", kernel.MaxLimit+1)
		storekit.Must(t, err)
		if len(batch) != kernel.DefaultLimit+1 {
			t.Fatalf("expanded bound: count=%d", len(batch))
		}
	}
	storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "corrupt", "reader"))
	storekit.Get[outbox.OutboxEntry](t)(s.Outbox().Append(ctx, outbox.OutboxEntry{
		Origin: "corrupt", Stream: outbox.ModuleEventStream, Created: time.Now(), Payload: []byte("invalid-envelope"),
	}))
	if _, _, err := e.Next(ctx, "corrupt", "reader", 1); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("corrupt stored event was dispatched: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.AppendEvent(canceled, businessEvent(0)); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled append: %v", err)
	}
	if _, err := e.RegisterConsumer(canceled, "instance", "reader"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled registration: %v", err)
	}
	if _, _, err := e.Next(canceled, "instance", "reader", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled read: %v", err)
	}
	if err := e.Ack(canceled, outbox.Position{Origin: "instance", Consumer: "reader"}, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled acknowledgement: %v", err)
	}
	if n, err := e.PruneAcknowledged(canceled, "instance", 1); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled prune: removed=%d err=%v", n, err)
	}
}

func concurrentEvents(t *testing.T, s EventContractStore) {
	ctx := t.Context()
	e := s.Events()
	storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "reader"))
	const count = 16
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for n := range count {
		wg.Go(func() { _, err := e.AppendEvent(ctx, businessEvent(n)); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		storekit.Must(t, err)
	}
	batch, _, err := e.Next(ctx, "instance", "reader", kernel.MaxLimit+1)
	storekit.Must(t, err)
	if len(batch) != count {
		t.Fatalf("concurrent event count: %d", len(batch))
	}
	for i, event := range batch {
		if event.Seq != int64(i+1) {
			t.Fatalf("concurrent identity gap/duplicate: %+v", batch)
		}
	}
}
