package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

type eventStore struct{ s *Store }

// Events returns module events over the existing ordered outbox and independent
// consumer positions. It shares the caller's metadata transaction.
func (s *Store) Events() outbox.EventStore { return eventStore{s} }

// lockStream serializes append/register/ack/prune without taking a domain lock.
// Consumer effects acquire their domain locks before acknowledgement calls it.
func (e eventStore) lockStream(ctx context.Context, origin string) (int64, error) {
	if _, err := e.s.Exec(ctx, "initialize module event sequence", dialect.InsertIgnore("outbox_sequences",
		[]string{"origin", "stream", "last_seq"}, []string{"origin", "stream"}), origin, outbox.ModuleEventStream, 0); err != nil {
		return 0, err
	}
	var floor int64
	err := e.s.Q(ctx).QueryRowContext(ctx, e.s.Dialect().Rebind(
		"SELECT pruned_through FROM outbox_sequences WHERE origin = ? AND stream = ?"+e.s.LockClause(ctx)),
		origin, outbox.ModuleEventStream).Scan(&floor)
	return floor, e.s.Wrap("lock module event stream", err)
}

func (e eventStore) position(ctx context.Context, origin, consumer string) (outbox.Position, error) {
	p := outbox.Position{Origin: origin, Consumer: consumer}
	err := e.s.Q(ctx).QueryRowContext(ctx, e.s.Dialect().Rebind(
		"SELECT acked_seq FROM outbox_consumers WHERE origin = ? AND stream = ? AND consumer = ?"),
		origin, outbox.ModuleEventStream, consumer).Scan(&p.Seq)
	return p, e.s.Wrap("read module event position", err)
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
		floor, err := e.lockStream(ctx, origin)
		if err != nil {
			return err
		}
		if _, err := e.s.Exec(ctx, "register module event consumer", dialect.InsertIgnore("outbox_consumers",
			[]string{"origin", "stream", "consumer", "acked_seq"}, []string{"origin", "stream", "consumer"}),
			origin, outbox.ModuleEventStream, consumer, floor); err != nil {
			return err
		}
		p, err = e.position(ctx, origin, consumer)
		return err
	})
	return p, err
}

func (e eventStore) Next(ctx context.Context, origin, consumer string, limit int) ([]outbox.Event, outbox.Position, error) {
	if err := outbox.ValidateConsumer(origin, consumer); err != nil {
		return nil, outbox.Position{}, fmt.Errorf("validate module event consumer: %w", err)
	}
	p, err := e.position(ctx, origin, consumer)
	if err != nil {
		return nil, outbox.Position{}, err
	}
	entries, err := outboxStore(e).Read(ctx, origin, outbox.ModuleEventStream, p.Seq, eventLimit(limit))
	if err != nil {
		return nil, outbox.Position{}, err
	}
	var events []outbox.Event
	for _, entry := range entries {
		// Under READ COMMITTED another worker can advance and prune between
		// the cursor and batch reads. Retry that snapshot before applying effects.
		if entry.Seq-p.Seq != int64(len(events))+1 {
			return nil, outbox.Position{}, fmt.Errorf("module event read position changed: %w", kernel.ErrConflict)
		}
		event, err := outbox.EventFromEntry(entry)
		if err != nil {
			return nil, outbox.Position{}, fmt.Errorf("decode module event: %w", err)
		}
		events = append(events, event)
	}
	return events, p, nil
}

func eventLimit(limit int) int {
	if limit <= 0 {
		return kernel.DefaultLimit
	}
	return min(limit, kernel.MaxLimit)
}

func (e eventStore) Ack(ctx context.Context, expected outbox.Position, eventSeq int64) error {
	if err := outbox.ValidateConsumer(expected.Origin, expected.Consumer); err != nil {
		return fmt.Errorf("validate module event consumer: %w", err)
	}
	if expected.Seq < 0 || eventSeq <= 0 || eventSeq-1 != expected.Seq {
		return fmt.Errorf("event acknowledgement must advance one position: %w", kernel.ErrConflict)
	}
	return e.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := e.lockStream(ctx, expected.Origin); err != nil {
			return err
		}
		current, err := e.position(ctx, expected.Origin, expected.Consumer)
		if err != nil {
			return err
		}
		if current.Seq != expected.Seq {
			return fmt.Errorf("event consumer position changed: %w", kernel.ErrConflict)
		}
		var exists bool
		err = e.s.Q(ctx).QueryRowContext(ctx, e.s.Dialect().Rebind(
			"SELECT EXISTS(SELECT 1 FROM outbox WHERE origin = ? AND stream = ? AND seq = ?)"),
			expected.Origin, outbox.ModuleEventStream, eventSeq).Scan(&exists)
		if err != nil {
			return e.s.Wrap("check acknowledged module event", err)
		}
		if !exists {
			return fmt.Errorf("acknowledged event does not exist: %w", kernel.ErrConflict)
		}
		return e.s.ExecOne(ctx, "acknowledge module event",
			"UPDATE outbox_consumers SET acked_seq = ? WHERE origin = ? AND stream = ? AND consumer = ? AND acked_seq = ?",
			eventSeq, expected.Origin, outbox.ModuleEventStream, expected.Consumer, expected.Seq)
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
		floor, err := e.lockStream(ctx, origin)
		if err != nil {
			return err
		}
		var minimum sql.NullInt64
		err = e.s.Q(ctx).QueryRowContext(ctx, e.s.Dialect().Rebind(
			"SELECT MIN(acked_seq) FROM outbox_consumers WHERE origin = ? AND stream = ?"),
			origin, outbox.ModuleEventStream).Scan(&minimum)
		if err != nil {
			return e.s.Wrap("read module event prune boundary", err)
		}
		if !minimum.Valid {
			return nil
		}
		through = min(through, minimum.Int64)
		if through <= floor {
			return nil
		}
		removed, err = e.s.Exec(ctx, "prune acknowledged module events",
			"DELETE FROM outbox WHERE origin = ? AND stream = ? AND seq <= ?", origin, outbox.ModuleEventStream, through)
		if err != nil {
			return err
		}
		return e.s.ExecOne(ctx, "retain module event floor",
			"UPDATE outbox_sequences SET pruned_through = ? WHERE origin = ? AND stream = ?",
			through, origin, outbox.ModuleEventStream)
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
