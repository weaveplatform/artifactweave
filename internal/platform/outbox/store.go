package outbox

import "context"

// OutboxStore persists the ordered event streams of decision 0021: this
// instance's outbox, each peer's acknowledged position in it, the receiver's
// applied positions, and blob fetches that failed while applying.
type OutboxStore interface {
	// Append stores e as the next entry of (e.Origin, e.Stream) and returns
	// it with Seq assigned, one more than the stream's last. Call it inside
	// the transaction that makes the change it records; concurrent appends
	// to one stream are serialised, never given the same Seq.
	Append(ctx context.Context, e OutboxEntry) (OutboxEntry, error)
	// Read returns up to limit entries of a stream with Seq above after, in
	// Seq order.
	Read(
		ctx context.Context,
		origin, stream string,
		after int64,
		limit int,
	) ([]OutboxEntry, error)
	// LastSeq returns the highest Seq of a stream, 0 when it has none.
	LastSeq(ctx context.Context, origin, stream string) (int64, error)
	// Streams returns the streams origin has entries or a sequence in,
	// ordered.
	Streams(ctx context.Context, origin string) ([]string, error)
	// Prune removes entries of a stream with Seq at most upTo and returns how
	// many. The stream's sequence is not reset. The reserved ModuleEventStream
	// additionally obeys EventStore.PruneAcknowledged, including its no-consumer
	// retention rule; peer streams keep their existing pruning behavior.
	Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error)

	// PutCursor creates or replaces a peer's position in a stream.
	PutCursor(ctx context.Context, c PeerCursor) error
	// Cursor returns a peer's position in a stream; kernel.ErrNotFound when
	// the peer has none there.
	Cursor(ctx context.Context, peer, stream string) (PeerCursor, error)
	// Cursors returns a peer's positions ("" for every peer) ordered by
	// (peer, stream).
	Cursors(ctx context.Context, peer string) ([]PeerCursor, error)
	// DeleteCursors removes every position of a peer; a peer with none is
	// not an error.
	DeleteCursors(ctx context.Context, peer string) error
	// DeleteCursor removes a peer's position in one stream, when the peer
	// stops exchanging it; none there is not an error.
	DeleteCursor(ctx context.Context, peer, stream string) error

	// Applied returns the receiver's position in a peer's stream; a zero
	// LastSeq and no error when nothing was applied yet.
	Applied(ctx context.Context, origin, stream string) (AppliedMarker, error)
	// LockApplied returns the receiver's position in a peer's stream, as
	// Applied does, and inside a transaction holds it until the transaction
	// ends. An applier reads its position with it, so that of two
	// transactions applying one origin's stream, which a retried batch and
	// the batch it retries can be, the second waits for the first and then
	// reads the position the first committed. Applied alone does not
	// serialise them on PostgreSQL, where both would read the same position
	// and apply the entry twice.
	LockApplied(ctx context.Context, origin, stream string) (AppliedMarker, error)
	// SetApplied records the position, in the transaction that applies the
	// entry, which is what makes applying idempotent.
	SetApplied(ctx context.Context, m AppliedMarker) error

	// RecordFailedFetch stores a failed blob fetch with Failures 1, or
	// increments Failures and replaces LastError of the existing one, keeping
	// its other fields, and returns the record as stored. Failures counts
	// the calls; the value in f is ignored.
	RecordFailedFetch(ctx context.Context, f FailedBlobFetch) (FailedBlobFetch, error)
	// RetryFailedFetch records another failed attempt only if the task still
	// exists. An operator deletion while network I/O was in flight must not
	// recreate it. kernel.ErrNotFound when it was deleted.
	RetryFailedFetch(ctx context.Context, stream, sha256, reason string) error
	// ResetFailedFetch starts operator replay at zero attempts without
	// changing the task's creation time. kernel.ErrNotFound if it is gone.
	ResetFailedFetch(ctx context.Context, stream, sha256 string) error
	// FailedFetches returns a stream's failed fetches ("" for every stream)
	// ordered by (stream, sha256).
	FailedFetches(ctx context.Context, stream string) ([]FailedBlobFetch, error)
	// FailedFetchPage returns at most limit failed fetches after offset rows,
	// ordered by (stream, sha256), and whether another row exists. The stream
	// filter precedes paging; an empty stream selects every stream.
	// kernel.ErrInvalid for limit below 1 or a negative offset.
	FailedFetchPage(ctx context.Context, stream string, limit, offset int) ([]FailedBlobFetch, bool, error)
	// ClearFailedFetch removes one after a successful retry;
	// kernel.ErrNotFound when there is none.
	ClearFailedFetch(ctx context.Context, stream, sha256 string) error
	// ClearFailedFetches drops every failed task of one nonempty stream,
	// when its repository lifetime ends. None there is not an error;
	// kernel.ErrInvalid for an empty stream, so no call clears all streams.
	ClearFailedFetches(ctx context.Context, stream string) error
}
