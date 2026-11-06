package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Table names of the peers group; the same names as the SQL tables.
const (
	tOutbox        = "outbox"
	tOutboxSeqs    = "outbox_sequences"
	tPeerCursors   = "peer_cursors"
	tApplied       = "applied_markers"
	tFailedFetches = "failed_blob_fetches"
)

// outboxKey identifies one outbox entry.
type outboxKey struct {
	origin, stream string
	seq            int64
}

// Outbox implements store.Store.
func (s *Store) Outbox() store.OutboxStore { return outboxStore{s} }

type outboxStore struct{ s *Store }

func (o outboxStore) entries() *table[outboxKey, types.OutboxEntry] {
	return tableOf[outboxKey, types.OutboxEntry](o.s, tOutbox)
}

func (o outboxStore) seqs() *table[peersPair, int64] {
	return tableOf[peersPair, int64](o.s, tOutboxSeqs)
}

func (o outboxStore) cursors() *table[peersPair, types.PeerCursor] {
	return tableOf[peersPair, types.PeerCursor](o.s, tPeerCursors)
}

func (o outboxStore) applied() *table[peersPair, types.AppliedMarker] {
	return tableOf[peersPair, types.AppliedMarker](o.s, tApplied)
}

func (o outboxStore) failed() *table[peersPair, types.FailedBlobFetch] {
	return tableOf[peersPair, types.FailedBlobFetch](o.s, tFailedFetches)
}

func (o outboxStore) Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error) {
	var out types.OutboxEntry
	err := o.s.do(ctx, func() error {
		k := peersPair{e.Origin, e.Stream}
		seq, _ := o.seqs().get(k)
		seq++
		o.seqs().put(k, seq)
		e.Seq = seq
		o.entries().put(outboxKey{e.Origin, e.Stream, seq}, e)
		out, _ = o.entries().get(outboxKey{e.Origin, e.Stream, seq})
		return nil
	})
	return out, err
}

func (o outboxStore) Read(
	ctx context.Context, origin, stream string, after int64, limit int,
) ([]types.OutboxEntry, error) {
	var out []types.OutboxEntry
	err := o.s.do(ctx, func() error {
		out = o.entries().all(func(k outboxKey, _ types.OutboxEntry) bool {
			return k.origin == origin && k.stream == stream && k.seq > after
		}, func(a, b types.OutboxEntry) int { return cmp.Compare(a.Seq, b.Seq) })
		out = out[:min(len(out), peersLimit(limit))]
		return nil
	})
	return out, err
}

// peersLimit applies the default limit for "at most limit" reads.
func peersLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}

func (o outboxStore) LastSeq(ctx context.Context, origin, stream string) (int64, error) {
	var seq int64
	err := o.s.do(ctx, func() error {
		seq, _ = o.seqs().get(peersPair{origin, stream})
		return nil
	})
	return seq, err
}

func (o outboxStore) Streams(ctx context.Context, origin string) ([]string, error) {
	var out []string
	err := o.s.do(ctx, func() error {
		for k := range o.seqs().rows {
			if k.a == origin {
				out = append(out, k.b)
			}
		}
		slices.Sort(out)
		return nil
	})
	return out, err
}

func (o outboxStore) Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error) {
	var n int64
	err := o.s.do(ctx, func() error {
		n = o.entries().deleteWhere(func(k outboxKey, _ types.OutboxEntry) bool {
			return k.origin == origin && k.stream == stream && k.seq <= upTo
		})
		return nil
	})
	return n, err
}

func (o outboxStore) PutCursor(ctx context.Context, c types.PeerCursor) error {
	return o.s.do(ctx, func() error {
		o.cursors().put(peersPair{c.PeerInstanceID, c.Stream}, c)
		return nil
	})
}

func (o outboxStore) Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error) {
	var out types.PeerCursor
	err := o.s.do(ctx, func() error {
		var ok bool
		if out, ok = o.cursors().get(peersPair{peer, stream}); !ok {
			return notFound("peer cursor", peer+" "+stream)
		}
		return nil
	})
	return out, err
}

func (o outboxStore) Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error) {
	var out []types.PeerCursor
	err := o.s.do(ctx, func() error {
		out = o.cursors().all(func(k peersPair, _ types.PeerCursor) bool {
			return peer == "" || k.a == peer
		}, func(a, b types.PeerCursor) int {
			return cmp.Or(
				strings.Compare(a.PeerInstanceID, b.PeerInstanceID),
				strings.Compare(a.Stream, b.Stream),
			)
		})
		return nil
	})
	return out, err
}

func (o outboxStore) DeleteCursors(ctx context.Context, peer string) error {
	return o.s.do(ctx, func() error {
		o.cursors().deleteWhere(func(k peersPair, _ types.PeerCursor) bool { return k.a == peer })
		return nil
	})
}

func (o outboxStore) Applied(
	ctx context.Context,
	origin, stream string,
) (types.AppliedMarker, error) {
	out := types.AppliedMarker{Origin: origin, Stream: stream}
	err := o.s.do(ctx, func() error {
		if m, ok := o.applied().get(peersPair{origin, stream}); ok {
			out = m
		}
		return nil
	})
	return out, err
}

func (o outboxStore) SetApplied(ctx context.Context, m types.AppliedMarker) error {
	return o.s.do(ctx, func() error {
		o.applied().put(peersPair{m.Origin, m.Stream}, m)
		return nil
	})
}

func (o outboxStore) RecordFailedFetch(
	ctx context.Context,
	f types.FailedBlobFetch,
) (types.FailedBlobFetch, error) {
	var out types.FailedBlobFetch
	err := o.s.do(ctx, func() error {
		k := peersPair{f.Stream, f.SHA256}
		cur, ok := o.failed().get(k)
		if ok {
			cur.Failures++
			cur.LastError = f.LastError
		} else {
			cur = f
			cur.Failures = 1
		}
		o.failed().put(k, cur)
		out, _ = o.failed().get(k)
		return nil
	})
	return out, err
}

func (o outboxStore) FailedFetches(
	ctx context.Context,
	stream string,
) ([]types.FailedBlobFetch, error) {
	var out []types.FailedBlobFetch
	err := o.s.do(ctx, func() error {
		out = o.failed().all(func(k peersPair, _ types.FailedBlobFetch) bool {
			return stream == "" || k.a == stream
		}, func(a, b types.FailedBlobFetch) int {
			return cmp.Or(strings.Compare(a.Stream, b.Stream), strings.Compare(a.SHA256, b.SHA256))
		})
		return nil
	})
	return out, err
}

func (o outboxStore) ClearFailedFetch(ctx context.Context, stream, sha256 string) error {
	return o.s.do(ctx, func() error {
		if !o.failed().del(peersPair{stream, sha256}) {
			return notFound("failed blob fetch", stream+" "+sha256)
		}
		return nil
	})
}
