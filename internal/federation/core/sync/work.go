package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// The engine's work kinds (decision 0033). The strings are persisted in
// work items, so they never change.
const (
	// SendKind is one dispatcher per peer and stream: it pushes the
	// stream's entries after the peer's acknowledged position.
	SendKind = "federation.stream-send"
	// PullKind reads one peer's stream for an instance it cannot push to.
	PullKind = "federation.stream-pull"
	// FetchKind fetches a blob whose fetch failed, with the queue's back-off,
	// and publishes the items waiting for it.
	FetchKind = "federation.stream-blob-fetch"
	// SweepKind runs every SweepInterval: it starts the dispatchers a
	// committed entry did not start, and the pulls.
	SweepKind     = "federation.stream-sweep"
	SweepInterval = 30 * time.Second
	// PruneKind runs every PruneInterval: it prunes acknowledged and
	// expired entries and flags the peers left behind.
	PruneKind     = "federation.stream-prune"
	PruneInterval = 10 * time.Minute
	// FullSyncKind is one per peer and stream: it resynchronises the
	// stream from the peer's snapshot (Resync), or probes whether it must.
	FullSyncKind = "federation.stream-full-sync"
	// periodicKey is the one item of each periodic kind.
	periodicKey = "streams"
)

// item is a work item's payload: the peer and stream of a dispatcher or a
// pull, or the stream and blob of a fetch. The key joins the same values,
// so the queue runs one item per pair, but is not parsed back.
type item struct {
	Peer   string `json:"peer,omitempty"`
	Stream string `json:"stream"`
	SHA256 string `json:"sha256,omitempty"`
}

func (i item) key() string {
	if i.SHA256 != "" {
		return i.Stream + " " + i.SHA256
	}
	return i.Peer + " " + i.Stream
}

// enqueue asks for kind's work on i, in the caller's transaction.
func (e *Engine) enqueue(ctx context.Context, kind string, i item) error {
	payload, _ := json.Marshal(i) //nolint:errchkjson // strings only
	if err := e.Queue.Enqueue(ctx, kind, i.key(), payload); err != nil {
		return fmt.Errorf("queue %s: %w", kind, err)
	}
	return nil
}

func decode(it work.WorkItem) (item, error) {
	var i item
	if err := json.Unmarshal(it.Payload, &i); err != nil || i.Stream == "" {
		return item{}, fmt.Errorf("%w: %s item %q has no stream", work.ErrPermanent, it.Kind, it.Key)
	}
	return i, nil
}

// Register gives the queue the engine's handlers and queues each periodic
// kind's item when it is not queued yet, so a restart adds none.
func (e *Engine) Register(ctx context.Context, q *work.Queue, items work.WorkQueue) error {
	handlers := map[string]func(context.Context, item) error{
		SendKind:  func(ctx context.Context, i item) error { return e.Dispatch(ctx, i.Peer, i.Stream) },
		PullKind:  func(ctx context.Context, i item) error { return e.PullFrom(ctx, i.Peer, i.Stream) },
		FetchKind: func(ctx context.Context, i item) error { return e.RetryFetch(ctx, i.Stream, i.SHA256) },
		FullSyncKind: func(ctx context.Context, i item) error {
			return e.Resync(ctx, i.Peer, i.Stream)
		},
	}
	for _, kind := range []string{SendKind, PullKind, FetchKind, FullSyncKind} {
		h := handlers[kind]
		if err := q.Handle(kind, func(ctx context.Context, it work.WorkItem) error {
			i, err := decode(it)
			if err != nil {
				return err
			}
			return h(ctx, i)
		}); err != nil {
			return fmt.Errorf("register %s: %w", kind, err)
		}
	}
	periodic := []struct {
		kind  string
		every time.Duration
		run   func(context.Context) error
	}{{SweepKind, SweepInterval, e.Sweep}, {PruneKind, PruneInterval, e.Prune}}
	for _, p := range periodic {
		again := func(ctx context.Context) error {
			if err := items.Enqueue(ctx, p.kind, periodicKey, nil, e.now()); err != nil {
				return fmt.Errorf("queue %s: %w", p.kind, err)
			}
			return nil
		}
		if err := q.HandleEvery(p.kind, p.every, func(ctx context.Context, _ work.WorkItem) error {
			if err := p.run(ctx); err != nil {
				return err
			}
			// The item is done once it returns; queueing the next keeps
			// the schedule going.
			return again(ctx)
		}); err != nil {
			return fmt.Errorf("register %s: %w", p.kind, err)
		}
		if _, err := items.Get(ctx, p.kind, periodicKey); errors.Is(err, kernel.ErrNotFound) {
			if err := again(ctx); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("read %s: %w", p.kind, err)
		}
	}
	return nil
}
