package events

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

const dispatchPrefix = "events.dispatch/"

// Store joins consumer effects, acknowledgements and the dispatch lease guard
// in the same metadata transaction. Queue and Bus must use this same store.
type Store interface {
	tx.Runner
	Events() outbox.EventStore
	WorkQueue() work.WorkQueue
}

// Handler commits database effects or a durable consumer-owned handoff with the
// event acknowledgement. It must use ctx's transaction and propagate failures.
// It must not perform network I/O or mutate ANY dispatch polling row: acquire
// all domain/delivery-work locks before returning to the bus's Ack/lease guard.
// A handoff must retain its own payload; an acknowledged outbox reference alone
// can be pruned before delivery. Delivery.Key is independent of entity keys.
type Handler func(ctx context.Context, delivery Delivery) error

// Options configures a bus for this instance's local event origin.
type Options struct {
	Origin string
	// Batch bounds one consumer's run. Zero uses 100; values above MaxLimit
	// or below zero are refused.
	Batch int
	// Poll is each consumer's independent queue timer. Zero uses one second.
	Poll time.Duration
	Now  func() time.Time
}

// Bus dispatches one ordered stream independently to each code-owned consumer.
// Register subscriptions and RegisterWork before running its queue. One bus
// owns a queue's local origin. Consumer names are bounded code constants, never
// tenant/webhook/subscription IDs, preserving the queue's metric cardinality.
type Bus struct {
	store Store
	queue *work.Queue
	o     Options
	mu    sync.Mutex
	// Registration freezes before persistence begins, so a retry after a
	// failed RegisterWork cannot race a changed set of topics/handlers.
	frozen, initialized bool
	subscriptions       map[string]map[string]Handler
}

// New builds a bus without I/O. It does not start a goroutine or own the queue's
// shutdown; the application drains and stops the existing work queue normally.
func New(store Store, queue *work.Queue, o Options) (*Bus, error) {
	if store == nil || queue == nil || o.Batch < 0 || o.Batch > kernel.MaxLimit || o.Poll < 0 {
		return nil, fmt.Errorf("event bus store, queue or options are invalid: %w", kernel.ErrInvalid)
	}
	if err := outbox.ValidateConsumer(o.Origin, "dispatch"); err != nil {
		return nil, fmt.Errorf("event bus origin: %w", err)
	}
	if o.Batch == 0 {
		o.Batch = 100
	}
	if o.Poll == 0 {
		o.Poll = time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Bus{store: store, queue: queue, o: o, subscriptions: map[string]map[string]Handler{}}, nil
}

// WorkKind is the reserved periodic queue kind for a code-owned consumer.
// Delivery/derived work uses another kind and Delivery.Key, never this kind.
func WorkKind(consumer string) string { return dispatchPrefix + consumer }

// Subscribe registers one topic handler for consumer. Different topics may
// share that consumer's ordered position; a duplicate topic is a conflict.
// Consumer names must also fit their reserved work kind's 64-byte bound.
func (b *Bus) Subscribe(topic, consumer string, handler Handler) error {
	if err := outbox.ValidateConsumer(b.o.Origin, consumer); err != nil {
		return fmt.Errorf("event subscription: %w", err)
	}
	if topic == "" || len(topic) > 128 || !utf8.ValidString(topic) || strings.IndexByte(topic, 0) >= 0 ||
		len(WorkKind(consumer)) > work.MaxWorkKind || handler == nil {
		return fmt.Errorf("event topic, consumer kind or handler is invalid: %w", kernel.ErrInvalid)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return fmt.Errorf("event subscriptions are frozen: %w", kernel.ErrConflict)
	}
	topics, exists := b.subscriptions[consumer]
	if _, duplicate := topics[topic]; duplicate {
		return fmt.Errorf("event subscription already exists: %w", kernel.ErrConflict)
	}
	if !exists {
		if err := b.queue.HandleEvery(WorkKind(consumer), b.o.Poll, func(ctx context.Context, item work.WorkItem) error {
			return b.dispatch(ctx, consumer, item)
		}); err != nil {
			return fmt.Errorf("event dispatch handler: %w", err)
		}
		topics = map[string]Handler{}
		b.subscriptions[consumer] = topics
	}
	topics[topic] = handler
	return nil
}

// RegisterWork atomically registers all consumers before any event can be
// pruned and asks for each recurring dispatch job. It is idempotent/retryable;
// a restart resumes stored positions and unparks work for known consumers.
func (b *Bus) RegisterWork(ctx context.Context) error {
	b.mu.Lock()
	b.frozen = true
	consumers := slices.Sorted(maps.Keys(b.subscriptions))
	b.mu.Unlock()
	err := b.store.Tx(ctx, func(ctx context.Context) error {
		for _, consumer := range consumers {
			if _, err := b.store.Events().RegisterConsumer(ctx, b.o.Origin, consumer); err != nil {
				return fmt.Errorf("register event consumer %q: %w", consumer, err)
			}
			if err := b.queue.Enqueue(ctx, WorkKind(consumer), b.o.Origin, nil); err != nil {
				return fmt.Errorf("schedule event consumer %q: %w", consumer, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("register event dispatch work: %w", err)
	}
	b.mu.Lock()
	b.initialized = true
	b.mu.Unlock()
	return nil
}

func (b *Bus) dispatch(ctx context.Context, consumer string, item work.WorkItem) error {
	b.mu.Lock()
	ready := b.initialized
	topics := maps.Clone(b.subscriptions[consumer])
	b.mu.Unlock()
	if !ready || item.Kind != WorkKind(consumer) || item.Key != b.o.Origin {
		return fmt.Errorf("event dispatch is not registered for this work: %w", kernel.ErrInvalid)
	}
	batch, position, err := b.store.Events().Next(ctx, b.o.Origin, consumer, b.o.Batch)
	if err != nil {
		return fmt.Errorf("read event consumer %q: %w", consumer, err)
	}
	for _, event := range batch {
		if err := b.apply(ctx, item, position, event, topics[event.Topic]); err != nil {
			return err
		}
		position.Seq = event.Seq
	}
	if _, err := b.store.Events().PruneAcknowledged(ctx, b.o.Origin, position.Seq); err != nil {
		return fmt.Errorf("prune acknowledged events: %w", err)
	}
	// Only successful runs mark their polling row dirty. Requeueing failures
	// would defeat the queue's backoff/poison-event parking. Even an empty
	// success retains the row and its monotonically increasing generation.
	if err := b.store.Tx(ctx, func(ctx context.Context) error {
		if err := b.guardLease(ctx, item); err != nil {
			return err
		}
		return b.queue.Enqueue(ctx, item.Kind, item.Key, nil)
	}); err != nil {
		return fmt.Errorf("reschedule event dispatch: %w", err)
	}
	return nil
}

func (b *Bus) apply(ctx context.Context, item work.WorkItem, expected outbox.Position, event outbox.Event, handler Handler) error {
	err := b.store.Tx(ctx, func(ctx context.Context) error {
		if handler != nil {
			delivery := Delivery{Consumer: expected.Consumer, Key: DeliveryKey(expected.Consumer, event), Event: event}
			delivery.Event.Payload = slices.Clone(event.Payload)
			if err := handler(ctx, delivery); err != nil {
				return fmt.Errorf("consume event %s/%d: %w", event.Origin, event.Seq, err)
			}
		}
		if err := b.store.Events().Ack(ctx, expected, event.Seq); err != nil {
			return fmt.Errorf("acknowledge event %s/%d: %w", event.Origin, event.Seq, err)
		}
		return b.guardLease(ctx, item)
	})
	if err != nil {
		return fmt.Errorf("commit event consumption: %w", err)
	}
	return nil
}

func (b *Bus) guardLease(ctx context.Context, item work.WorkItem) error {
	until, err := b.store.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
	if err != nil {
		return fmt.Errorf("guard event dispatch lease: %w", err)
	}
	// Read the clock after a possible row-lock wait. Checking against a
	// pre-wait timestamp could let an expired dispatcher commit.
	if !b.o.Now().Before(until) {
		return fmt.Errorf("event dispatch lease expired: %w", kernel.ErrConflict)
	}
	return nil
}
