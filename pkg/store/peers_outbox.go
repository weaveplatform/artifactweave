package store

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// OutboxStore persists the ordered event streams of decision 0021: this
// instance's outbox, each peer's acknowledged position in it, the receiver's
// applied positions, and blob fetches that failed while applying.
type OutboxStore interface {
	// Append stores e as the next entry of (e.Origin, e.Stream) and returns
	// it with Seq assigned, one more than the stream's last. Call it inside
	// the transaction that makes the change it records; concurrent appends
	// to one stream are serialised, never given the same Seq.
	Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error)
	// Read returns up to limit entries of a stream with Seq above after, in
	// Seq order.
	Read(
		ctx context.Context,
		origin, stream string,
		after int64,
		limit int,
	) ([]types.OutboxEntry, error)
	// LastSeq returns the highest Seq of a stream, 0 when it has none.
	LastSeq(ctx context.Context, origin, stream string) (int64, error)
	// Streams returns the streams origin has entries or a sequence in,
	// ordered.
	Streams(ctx context.Context, origin string) ([]string, error)
	// Prune removes entries of a stream with Seq at most upTo and returns how
	// many. The stream's sequence is not reset.
	Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error)

	// PutCursor creates or replaces a peer's position in a stream.
	PutCursor(ctx context.Context, c types.PeerCursor) error
	// Cursor returns a peer's position in a stream.
	Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error)
	// Cursors returns a peer's positions ("" for every peer) ordered by
	// (peer, stream).
	Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error)
	// DeleteCursors removes every position of a peer; a peer with none is
	// not an error.
	DeleteCursors(ctx context.Context, peer string) error

	// Applied returns the receiver's position in a peer's stream; a zero
	// LastSeq and no error when nothing was applied yet.
	Applied(ctx context.Context, origin, stream string) (types.AppliedMarker, error)
	// SetApplied records the position, in the transaction that applies the
	// entry, which is what makes applying idempotent.
	SetApplied(ctx context.Context, m types.AppliedMarker) error

	// RecordFailedFetch stores a failed blob fetch with Failures 1, or
	// increments Failures and replaces LastError of the existing one, keeping
	// its other fields, and returns the record as stored. Failures counts
	// the calls; the value in f is ignored.
	RecordFailedFetch(ctx context.Context, f types.FailedBlobFetch) (types.FailedBlobFetch, error)
	// FailedFetches returns a stream's failed fetches ("" for every stream)
	// ordered by (stream, sha256).
	FailedFetches(ctx context.Context, stream string) ([]types.FailedBlobFetch, error)
	// ClearFailedFetch removes one after a successful retry; ErrNotFound
	// when there is none.
	ClearFailedFetch(ctx context.Context, stream, sha256 string) error
}
