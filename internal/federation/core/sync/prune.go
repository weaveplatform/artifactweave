package sync

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

// Sweep starts what a committed entry did not (SweepKind): the dispatcher
// of every peer this instance pushes to whose cursor is behind the stream's
// last entry, a peer added after the entry for one, and a pull of every
// stream this instance pulls, and the run of every stream this instance
// is resynchronising, so a run whose work was given up on goes on. Streams
// go in the order of Priority, highest first, so FED-04's prioritization
// dispatches them first.
func (e *Engine) Sweep(ctx context.Context) error {
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	flags, err := e.Store.Resyncs().Resyncs(ctx, self)
	if err != nil {
		return fmt.Errorf("full-sync flags: %w", err)
	}
	for _, f := range flags {
		if f.Receiver != self {
			continue
		}
		if err := e.enqueue(ctx, FullSyncKind, item{Peer: f.Origin, Stream: f.Stream}); err != nil {
			return err
		}
	}
	for _, k := range e.Kinds.Kinds() {
		streams, err := e.ordered(ctx, k)
		if err != nil {
			return err
		}
		for _, stream := range streams {
			if err := e.sweepStream(ctx, self, k, stream); err != nil {
				return err
			}
		}
	}
	return nil
}

// ordered is k's streams in the order of Priority, highest first, and by
// name among equals.
func (e *Engine) ordered(ctx context.Context, k Kind) ([]string, error) {
	streams, err := k.Streams(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s streams: %w", k.Name(), err)
	}
	streams = slices.Clone(streams)
	priority := map[string]int{}
	if e.Priority != nil {
		for _, s := range streams {
			priority[s] = e.Priority(ctx, s)
		}
	}
	slices.SortStableFunc(streams, func(a, b string) int {
		return cmp.Or(cmp.Compare(priority[b], priority[a]), strings.Compare(a, b))
	})
	return streams, nil
}

func (e *Engine) sweepStream(ctx context.Context, self string, k Kind, stream string) error {
	subs, err := k.Subscribers(ctx, stream)
	if err != nil {
		return fmt.Errorf("stream %s subscribers: %w", stream, err)
	}
	head, err := e.Store.Outbox().LastSeq(ctx, self, stream)
	if err != nil {
		return fmt.Errorf("stream %s head: %w", stream, err)
	}
	for _, id := range subs {
		p, ok, err := e.peer(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if p.Pulls() && holds(p, k, Receive) {
			if err := e.enqueue(ctx, PullKind, item{Peer: id, Stream: stream}); err != nil {
				return err
			}
		}
		if !p.Pushes() || !holds(p, k, Send) {
			continue
		}
		c, err := e.cursor(ctx, id, stream)
		if err != nil {
			return err
		}
		if c.AckedSeq < head {
			if err := e.enqueue(ctx, SendKind, item{Peer: id, Stream: stream}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Prune removes this instance's entries that every peer it sends the
// stream to has acknowledged, and those older than the retention window
// (PruneKind). A peer whose position the window has passed is flagged for
// a full sync (PEER-05); the numbering is never reset, so the peer's next
// push or pull finds the gap rather than other entries under its numbers.
// Snapshots no receiver has read for SnapshotIdle go too.
func (e *Engine) Prune(ctx context.Context) error {
	self, err := e.self(ctx)
	if err != nil {
		return err
	}
	if _, err := e.Store.Snapshots().PruneSnapshots(ctx, e.now().Add(-SnapshotIdle)); err != nil {
		return fmt.Errorf("prune snapshots: %w", err)
	}
	cutoff := e.now().Add(-e.retention())
	for _, k := range e.Kinds.Kinds() {
		streams, err := k.Streams(ctx)
		if err != nil {
			return fmt.Errorf("%s streams: %w", k.Name(), err)
		}
		for _, stream := range streams {
			if err := e.pruneStream(ctx, self, k, stream, cutoff); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) pruneStream(ctx context.Context, self string, k Kind, stream string, cutoff time.Time) error {
	head, err := e.Store.Outbox().LastSeq(ctx, self, stream)
	if err != nil {
		return fmt.Errorf("stream %s head: %w", stream, err)
	}
	subs, err := k.Subscribers(ctx, stream)
	if err != nil {
		return fmt.Errorf("stream %s subscribers: %w", stream, err)
	}
	acked := head
	var receivers []model.Peer
	positions := map[string]int64{}
	for _, id := range subs {
		p, ok, err := e.peer(ctx, id)
		if err != nil {
			return err
		}
		if !ok || !holds(p, k, Send) {
			continue
		}
		c, err := e.cursor(ctx, id, stream)
		if err != nil {
			return err
		}
		receivers = append(receivers, p)
		positions[id] = c.AckedSeq
		acked = min(acked, c.AckedSeq)
	}
	expired, err := e.expired(ctx, self, stream, cutoff)
	if err != nil {
		return err
	}
	upTo := max(acked, expired)
	if upTo <= 0 {
		return nil
	}
	n, err := e.Store.Outbox().Prune(ctx, self, stream, upTo)
	if err != nil {
		return fmt.Errorf("prune %s through %d: %w", stream, upTo, err)
	}
	if n == 0 {
		// Nothing newly gone, so no peer newly behind: the flags of an
		// earlier prune stand as they were.
		return nil
	}
	for _, p := range receivers {
		if positions[p.InstanceID] < upTo {
			if err := e.flag(ctx, self, p.InstanceID, stream, positions[p.InstanceID]); err != nil {
				return err
			}
		}
	}
	return nil
}

// expired is the highest sequence of stream's leading entries created
// before cutoff, 0 when the first is not. Entries are read from the start
// of what is retained, which is what the prune is about to remove plus at
// most one page.
func (e *Engine) expired(ctx context.Context, self, stream string, cutoff time.Time) (int64, error) {
	var after, out int64
	for {
		entries, err := e.Store.Outbox().Read(ctx, self, stream, after, MaxBatch)
		if err != nil {
			return 0, fmt.Errorf("read %s after %d: %w", stream, after, err)
		}
		for _, en := range entries {
			if !en.Created.Before(cutoff) {
				return out, nil
			}
			out = en.Seq
		}
		if len(entries) < MaxBatch {
			return out, nil
		}
		after = out
	}
}
