package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	gosync "sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Defaults for Engine fields left zero.
const (
	// DefaultRetention is decision 0021's outbox retention window.
	DefaultRetention = 7 * 24 * time.Hour
	// DefaultBatch is the peer API's default page size; MaxBatch its
	// largest batch and page (api/src/peer, EventBatch.events maxItems).
	DefaultBatch = 100
	MaxBatch     = 1000
	// runBatches bounds the batches or pages one run sends or reads before
	// it hands the rest to its next run, so one busy stream cannot hold a
	// worker while others wait.
	runBatches = 10
	// SnapshotIdle is how long a snapshot is kept after its last page was
	// served. A receiver that comes back later is answered 410 and takes a
	// new one, which is cheaper than holding every abandoned copy.
	SnapshotIdle = time.Hour
)

var (
	// ErrNoStream refuses a stream this instance does not exchange with
	// the caller, or content no such stream refers to: 404.
	ErrNoStream = errors.New("this instance does not exchange the stream with the caller")
	// ErrRefused refuses a peer that does not hold the stream's use case,
	// or a batch whose origin is not the caller: 403.
	ErrRefused = errors.New("the caller may not exchange the stream with this instance")
	// ErrGone reports entries after a position that the origin has pruned;
	// the receiver needs a full sync: 410. A snapshot cursor whose
	// snapshot is no longer kept answers it too.
	ErrGone = errors.New("the entries after the position have been pruned")
	// ErrBusy refuses a batch on a stream this instance is resynchronising
	// from the caller: its entries apply through the full sync until it
	// completes, so the caller retries later: 503.
	ErrBusy = errors.New("a full sync of the stream from the caller is in progress")
	// ErrNoSnapshot refuses a snapshot, or a full sync, of a stream whose
	// kind takes none: 404.
	ErrNoSnapshot = errors.New("this instance takes no snapshots of the stream")
	// errApplied reports an entry another transaction applied first.
	errApplied = errors.New("entry already applied")
	// errSize reports fetched content of another size than its entry gives.
	errSize = errors.New("blob size mismatch")
	// errAhead reports a peer that expects entries this outbox never held.
	errAhead = errors.New("peer is ahead of this outbox")
)

// ConflictError refuses a batch that does not start at the sequence the
// receiver expects next: 409 with expected_seq.
type ConflictError struct{ Expected int64 }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("the next batch must start at sequence %d", e.Expected)
}

// Batch is one push: consecutive entries of origin's stream from FromSeq.
type Batch struct {
	Origin  string
	Stream  string
	FromSeq int64
	Entries []outbox.OutboxEntry
}

// Page is one pull answer: the origin, entries after the position asked
// for, and the position to ask from next, 0 when none remain.
type Page struct {
	Origin    string
	Entries   []outbox.OutboxEntry
	NextAfter int64
}

// SnapshotPage is one page of a peer's snapshot of a stream: entries in
// order of key, taken at Sequence, and the cursor of the next page, ""
// after the last.
type SnapshotPage struct {
	Origin     string
	Stream     string
	Sequence   int64
	Entries    []model.SnapshotEntry
	NextCursor string
}

// Remote calls a peer's stream and blob operations through the signed peer
// client (adapters/peer).
type Remote interface {
	// Push sends b and returns the highest sequence the peer has applied
	// from b.Origin on the stream; a *ConflictError carries a 409's
	// expected sequence.
	Push(ctx context.Context, peer model.Peer, b Batch) (int64, error)
	// Pull reads the peer's entries on stream after after; ErrGone on 410.
	Pull(ctx context.Context, peer model.Peer, stream string, after int64, limit int) (Page, error)
	// Snapshot reads a page of the peer's snapshot of stream: a new
	// snapshot when cursor is "", otherwise the page cursor continues;
	// ErrGone on 410.
	Snapshot(ctx context.Context, peer model.Peer, stream, cursor string, limit int) (SnapshotPage, error)
	// FetchBlob streams the peer's blob sha256 to into once the answer's
	// signature verifies. into's error is returned as it is.
	FetchBlob(ctx context.Context, peer model.Peer, sha256 string, into func(io.Reader) error) error
}

// Store is what the engine reads and writes, in one unit of work: the peer
// records, the outbox with its cursors, applied markers and failed fetches,
// the full-sync flags, the snapshots taken for receivers and the full-sync
// runs, and this instance's identity.
type Store interface {
	tx.Runner
	Peers() port.PeerStore
	Outbox() outbox.OutboxStore
	Resyncs() port.StreamResyncStore
	Snapshots() port.SnapshotStore
	Federation() port.FederationStore
	Instance() instance.InstanceStore
}

// Engine delivers this instance's stream entries to its peers and applies
// theirs, in order and exactly once (decision 0021, Ordered outbox).
type Engine struct {
	Store  Store
	Kinds  *Registry
	Blobs  blob.Store
	Remote Remote
	// Queue runs the dispatchers, pulls and blob retries (Register).
	Queue *work.Queue
	// Retention is how long an entry is kept for a peer that has not
	// acknowledged it; zero is DefaultRetention.
	Retention time.Duration
	// Batch is the entries one push or pull carries; zero is DefaultBatch.
	Batch int
	// Priority orders dispatch: a stream with a higher value is dispatched
	// before one with a lower value. FED-04's prioritization sets it; nil
	// gives every stream the same.
	Priority func(ctx context.Context, stream string) int
	Now      func() time.Time

	clockMu gosync.Mutex
	clock   outbox.HLC
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) retention() time.Duration {
	if e.Retention > 0 {
		return e.Retention
	}
	return DefaultRetention
}

func (e *Engine) batch() int {
	if e.Batch > 0 {
		return min(e.Batch, MaxBatch)
	}
	return DefaultBatch
}

// self is this instance's id, the origin of its own entries.
func (e *Engine) self(ctx context.Context) (string, error) {
	id, err := instance.EnsureID(ctx, e.Store)
	if err != nil {
		return "", fmt.Errorf("stream origin: %w", err)
	}
	return id, nil
}

// Self is this instance's id, the origin of its own entries: what a kind
// stamps an item with when no change of the stream has stamped it yet.
func (e *Engine) Self(ctx context.Context) (string, error) { return e.self(ctx) }

// tick is the clock time of a local change, never behind any time the
// clock has given or seen (decision 0021, Conflict rule).
func (e *Engine) tick() outbox.HLC {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	e.clock = e.clock.Next(e.now())
	return e.clock
}

// observe merges a received entry's time into the clock, so a later local
// change orders after it.
func (e *Engine) observe(t outbox.HLC) {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	e.clock = e.clock.Merge(t, e.now())
}

// authorize returns the kind of stream when peer may exchange it with this
// instance in direction d: ErrNoStream for a stream of no registered kind
// or one the kind does not exchange with peer, ErrRefused for a peer
// without the kind's use case for d.
func (e *Engine) authorize(ctx context.Context, peer model.Peer, stream string, d Direction) (Kind, error) {
	k, ok := e.Kinds.For(stream)
	if !ok {
		return nil, ErrNoStream
	}
	if !holds(peer, k, d) {
		return nil, ErrRefused
	}
	subs, err := k.Subscribers(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("stream %s subscribers: %w", stream, err)
	}
	if !slices.Contains(subs, peer.InstanceID) {
		return nil, ErrNoStream
	}
	return k, nil
}

// regrant reads the peer's record again and authorizes it, inside the
// apply transaction: a trust cancelled since the batch arrived, or a
// subscription ended, stops the apply (decision 0021: the grant is checked
// again when an event is applied).
func (e *Engine) regrant(ctx context.Context, peerID, stream string, d Direction) (Kind, error) {
	p, err := e.Store.Peers().Get(ctx, peerID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, ErrRefused
	}
	if err != nil {
		return nil, fmt.Errorf("read peer %s: %w", peerID, err)
	}
	return e.authorize(ctx, p, stream, d)
}

// peer reads a peer's record; ok is false when there is none.
func (e *Engine) peer(ctx context.Context, id string) (model.Peer, bool, error) {
	p, err := e.Store.Peers().Get(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.Peer{}, false, nil
	}
	if err != nil {
		return model.Peer{}, false, fmt.Errorf("read peer %s: %w", id, err)
	}
	return p, true, nil
}

// cursor is peer's acknowledged position on stream, 0 when it has none.
func (e *Engine) cursor(ctx context.Context, peer, stream string) (outbox.PeerCursor, error) {
	c, err := e.Store.Outbox().Cursor(ctx, peer, stream)
	if errors.Is(err, kernel.ErrNotFound) {
		return outbox.PeerCursor{PeerInstanceID: peer, Stream: stream}, nil
	}
	if err != nil {
		return outbox.PeerCursor{}, fmt.Errorf("peer %s cursor on %s: %w", peer, stream, err)
	}
	return c, nil
}

// flagged reports whether origin's stream is flagged for a full sync of
// receiver: on the origin, that it sends receiver nothing but the
// resynchronisation; on the receiver, that the origin's entries apply only
// through its run.
func (e *Engine) flagged(ctx context.Context, origin, receiver, stream string) (model.StreamResync, bool, error) {
	f, err := e.Store.Resyncs().Get(ctx, origin, receiver, stream)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.StreamResync{}, false, nil
	}
	if err != nil {
		return model.StreamResync{}, false, fmt.Errorf("full-sync flag of %s on %s: %w", receiver, stream, err)
	}
	return f, true, nil
}

// flag records that receiver's position on origin's stream is below the
// origin's pruned floor, or that a snapshot was taken for it, so the origin
// sends it nothing more until it has caught up (PEER-05).
func (e *Engine) flag(ctx context.Context, origin, receiver, stream string, position int64) error {
	if err := e.Store.Resyncs().Flag(ctx, model.StreamResync{
		Origin: origin, Receiver: receiver, Stream: stream, Position: position, Flagged: e.now(),
	}); err != nil {
		return fmt.Errorf("flag %s for a full sync of %s: %w", receiver, stream, err)
	}
	return nil
}
