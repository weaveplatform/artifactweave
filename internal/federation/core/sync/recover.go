package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RecoverKind is one per stream: it retries at once the delivery of the
// stream to its peers that the queue is backing off from (Recover). The
// string is persisted in work items, so it never changes.
const RecoverKind = "federation.stream-recover"

// PriorityLead is how much earlier than now a stream of priority 1 queues
// its dispatchers and pulls: the queue hands out the longest due first, so
// they go ahead of every standard stream's queued within the lead. An hour
// is far longer than any dispatcher waits in a queue that keeps up, and
// short enough that a standard stream left waiting by a stream of high
// priority still goes before that stream's work queued an hour later.
const PriorityLead = time.Hour

// recovery is a RecoverKind item's payload.
type recovery struct {
	Stream string `json:"stream"`
	Force  bool   `json:"force,omitempty"`
}

// QueueRecovery asks, in the caller's transaction, for stream's delivery
// to be retried at once (Recover). A forced request is queued under a key
// of its own, so a plain one queued after it cannot take its force away.
func (e *Engine) QueueRecovery(ctx context.Context, stream string, force bool) error {
	key := stream
	if force {
		key = "force " + stream
	}
	payload, _ := json.Marshal(recovery{Stream: stream, Force: force}) //nolint:errchkjson // a string and a bool
	if err := e.Queue.Enqueue(ctx, RecoverKind, key, payload); err != nil {
		return fmt.Errorf("queue %s: %w", RecoverKind, err)
	}
	return nil
}

// Recover retries at once the delivery of stream to each peer that the
// queue is backing off from, which 7.x's recovery calls an exhausted
// queue: a dispatcher whose last push failed, or every dispatcher and pull
// of the stream when force is set. Each resumes from the peer's
// acknowledged cursor, so nothing applies twice. The runs are the queue's
// own items, run now through EnsureFresh, so a run already in progress
// elsewhere is waited for, never doubled; a run that fails again is left
// to the queue's back-off, and recovery goes on to the next peer.
func (e *Engine) Recover(ctx context.Context, stream string, force bool) error {
	k, ok := e.Kinds.For(stream)
	if !ok {
		return nil
	}
	type run struct {
		kind string
		item
	}
	var due []run
	err := e.Store.Tx(ctx, func(ctx context.Context) error {
		subs, err := k.Subscribers(ctx, stream)
		if err != nil {
			return fmt.Errorf("stream %s subscribers: %w", stream, err)
		}
		for _, id := range subs {
			p, ok, err := e.peer(ctx, id)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if p.Pulls() && holds(p, k, Receive) && force {
				due = append(due, run{PullKind, item{Peer: id, Stream: stream}})
			}
			if !p.Pushes() || !holds(p, k, Send) {
				continue
			}
			c, err := e.cursor(ctx, id, stream)
			if err != nil {
				return err
			}
			if !force && c.LastError == "" {
				continue
			}
			due = append(due, run{SendKind, item{Peer: id, Stream: stream}})
		}
		for _, i := range due {
			// Enqueued first, so a parked item is pending again and one
			// that has none gets one: EnsureFresh runs only what is queued.
			if err := e.enqueue(ctx, i.kind, i.item); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the callback names its failures
	}
	for _, i := range due {
		// The run's failure is the queue's to retry; recovery answers for
		// having tried.
		_ = e.Queue.EnsureFresh(ctx, i.kind, i.key())
	}
	return nil
}

// Forget ends this instance's exchange of stream with peer, which no
// longer subscribes to it: a federated repository's removed member. The
// peer's acknowledged position and the full-sync flags and run between
// the two go, so should the peer subscribe again it starts afresh, and
// nothing holds the stream's pruning or its pushes back for it. The
// caller's transaction carries it.
func (e *Engine) Forget(ctx context.Context, peer, stream string) error {
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	if f, ok, err := e.flagged(ctx, peer, self, stream); err != nil {
		return err
	} else if ok {
		if err := e.stop(ctx, f, "the peer no longer exchanges the stream with this instance"); err != nil {
			return err
		}
	}
	if err := e.Store.Resyncs().Clear(ctx, self, peer, stream); err != nil {
		return fmt.Errorf("clear %s's full-sync flag on %s: %w", peer, stream, err)
	}
	if err := e.Store.Outbox().DeleteCursor(ctx, peer, stream); err != nil {
		return fmt.Errorf("delete %s's cursor on %s: %w", peer, stream, err)
	}
	return nil
}

// lead is how far ahead of now stream's dispatchers and pulls are queued:
// PriorityLead for each step of its Priority.
func (e *Engine) lead(ctx context.Context, stream string) time.Duration {
	if e.Priority == nil {
		return 0
	}
	return time.Duration(max(e.Priority(ctx, stream), 0)) * PriorityLead
}

// PruneEvery is how often pruning runs: PruneInterval, or a sixth of a
// shorter retention window, so an entry is pruned soon after it expires
// rather than up to PruneInterval later.
func (e *Engine) PruneEvery() time.Duration {
	return min(PruneInterval, max(e.retention()/6, time.Second))
}
