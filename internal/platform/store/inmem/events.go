package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

type eventConsumerKey struct{ origin, consumer string }

type eventStore struct{ s *Store }

// Events returns the general module-event facade over the existing outbox.
func (s *Store) Events() outbox.EventStore { return eventStore{s} }

func (e eventStore) consumers() *memtx.Table[eventConsumerKey, outbox.Position] {
	return memtx.TableOf[eventConsumerKey, outbox.Position](e.s.Store, "outbox_consumers")
}

func (e eventStore) floors() *memtx.Table[string, int64] {
	return memtx.TableOf[string, int64](e.s.Store, "outbox_retained_floors")
}

func (e eventStore) ensureStream(origin string) {
	ob := outboxStore(e)
	key := peersPair{origin, outbox.ModuleEventStream}
	if !ob.seqs().Has(key) {
		ob.seqs().Put(key, 0)
	}
}

func (e eventStore) position(origin, consumer string) (outbox.Position, error) {
	p, ok := e.consumers().Get(eventConsumerKey{origin, consumer})
	if !ok {
		return outbox.Position{}, fmt.Errorf("event consumer %q: %w", consumer, kernel.ErrNotFound)
	}
	return p, nil
}

func (e eventStore) AppendEvent(ctx context.Context, event outbox.Event) (outbox.Event, error) {
	entry, err := outbox.EventEntry(event)
	if err != nil {
		return outbox.Event{}, fmt.Errorf("encode module event: %w", err)
	}
	var committed outbox.Event
	err = e.s.Tx(ctx, func(ctx context.Context) error {
		stored, err := outboxStore(e).Append(ctx, entry)
		if err != nil {
			return err
		}
		decoded, err := outbox.EventFromEntry(stored)
		if err != nil {
			return fmt.Errorf("decode module event: %w", err)
		}
		committed = decoded
		return nil
	})
	if err != nil {
		return outbox.Event{}, err
	}
	return committed, nil
}

func (e eventStore) RegisterConsumer(ctx context.Context, origin, consumer string) (outbox.Position, error) {
	if err := outbox.ValidateConsumer(origin, consumer); err != nil {
		return outbox.Position{}, fmt.Errorf("validate module event consumer: %w", err)
	}
	var p outbox.Position
	err := e.s.Tx(ctx, func(ctx context.Context) error {
		e.ensureStream(origin)
		key := eventConsumerKey{origin, consumer}
		if got, ok := e.consumers().Get(key); ok {
			p = got
			return nil
		}
		floor, _ := e.floors().Get(origin)
		p = outbox.Position{Origin: origin, Consumer: consumer, Seq: floor}
		e.consumers().Put(key, p)
		return nil
	})
	return p, err
}

func (e eventStore) Next(ctx context.Context, origin, consumer string, limit int) ([]outbox.Event, outbox.Position, error) {
	if err := outbox.ValidateConsumer(origin, consumer); err != nil {
		return nil, outbox.Position{}, fmt.Errorf("validate module event consumer: %w", err)
	}
	var events []outbox.Event
	var p outbox.Position
	err := e.s.Tx(ctx, func(ctx context.Context) error {
		var err error
		p, err = e.position(origin, consumer)
		if err != nil {
			return err
		}
		entries, err := outboxStore(e).Read(ctx, origin, outbox.ModuleEventStream, p.Seq, limitOr(limit))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Seq-p.Seq != int64(len(events))+1 {
				return fmt.Errorf("module event read position changed: %w", kernel.ErrConflict)
			}
			event, err := outbox.EventFromEntry(entry)
			if err != nil {
				return fmt.Errorf("decode module event: %w", err)
			}
			events = append(events, event)
		}
		return nil
	})
	if err != nil {
		return nil, outbox.Position{}, err
	}
	return events, p, nil
}

func (e eventStore) Ack(ctx context.Context, expected outbox.Position, eventSeq int64) error {
	if err := outbox.ValidateConsumer(expected.Origin, expected.Consumer); err != nil {
		return fmt.Errorf("validate module event consumer: %w", err)
	}
	if expected.Seq < 0 || eventSeq <= 0 || eventSeq-1 != expected.Seq {
		return fmt.Errorf("event acknowledgement must advance one position: %w", kernel.ErrConflict)
	}
	return e.s.Tx(ctx, func(ctx context.Context) error {
		current, err := e.position(expected.Origin, expected.Consumer)
		if err != nil {
			return err
		}
		if current.Seq != expected.Seq {
			return fmt.Errorf("event consumer position changed: %w", kernel.ErrConflict)
		}
		ob := outboxStore(e)
		if !ob.entries().Has(outboxKey{expected.Origin, outbox.ModuleEventStream, eventSeq}) {
			return fmt.Errorf("acknowledged event does not exist: %w", kernel.ErrConflict)
		}
		current.Seq = eventSeq
		e.consumers().Put(eventConsumerKey{current.Origin, current.Consumer}, current)
		return nil
	})
}

func (e eventStore) PruneAcknowledged(ctx context.Context, origin string, through int64) (int64, error) {
	if err := outbox.ValidateConsumer(origin, "pruning"); err != nil {
		return 0, fmt.Errorf("validate module event origin: %w", err)
	}
	if through < 0 {
		return 0, fmt.Errorf("event prune position cannot be negative: %w", kernel.ErrInvalid)
	}
	var removed int64
	err := e.s.Tx(ctx, func(ctx context.Context) error {
		e.ensureStream(origin)
		floor, _ := e.floors().Get(origin)
		if through <= floor {
			return nil
		}
		found := false
		for key, p := range e.consumers().Rows {
			if key.origin == origin {
				through = min(through, p.Seq)
				found = true
			}
		}
		if !found || through <= floor {
			return nil
		}
		ob := outboxStore(e)
		removed = ob.entries().DeleteWhere(func(key outboxKey, _ outbox.OutboxEntry) bool {
			return key.origin == origin && key.stream == outbox.ModuleEventStream && key.seq <= through
		})
		e.floors().Put(origin, through)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
