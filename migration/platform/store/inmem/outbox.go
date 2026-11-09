package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
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

func (o outboxStore) entries() *memtx.Table[outboxKey, types.OutboxEntry] {
	return memtx.TableOf[outboxKey, types.OutboxEntry](o.s.Store, tOutbox)
}

func (o outboxStore) seqs() *memtx.Table[peersPair, int64] {
	return memtx.TableOf[peersPair, int64](o.s.Store, tOutboxSeqs)
}

func (o outboxStore) cursors() *memtx.Table[peersPair, types.PeerCursor] {
	return memtx.TableOf[peersPair, types.PeerCursor](o.s.Store, tPeerCursors)
}

func (o outboxStore) applied() *memtx.Table[peersPair, types.AppliedMarker] {
	return memtx.TableOf[peersPair, types.AppliedMarker](o.s.Store, tApplied)
}

func (o outboxStore) failed() *memtx.Table[peersPair, types.FailedBlobFetch] {
	return memtx.TableOf[peersPair, types.FailedBlobFetch](o.s.Store, tFailedFetches)
}

func (o outboxStore) Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error) {
	var out types.OutboxEntry
	err := o.s.Do(ctx, func() error {
		k := peersPair{e.Origin, e.Stream}
		seq, _ := o.seqs().Get(k)
		seq++
		o.seqs().Put(k, seq)
		e.Seq = seq
		o.entries().Put(outboxKey{e.Origin, e.Stream, seq}, e)
		out, _ = o.entries().Get(outboxKey{e.Origin, e.Stream, seq})
		return nil
	})
	return out, err
}

func (o outboxStore) Read(
	ctx context.Context, origin, stream string, after int64, limit int,
) ([]types.OutboxEntry, error) {
	var out []types.OutboxEntry
	err := o.s.Do(ctx, func() error {
		out = o.entries().All(func(k outboxKey, _ types.OutboxEntry) bool {
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
	err := o.s.Do(ctx, func() error {
		seq, _ = o.seqs().Get(peersPair{origin, stream})
		return nil
	})
	return seq, err
}

func (o outboxStore) Streams(ctx context.Context, origin string) ([]string, error) {
	var out []string
	err := o.s.Do(ctx, func() error {
		for k := range o.seqs().Rows {
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
	err := o.s.Do(ctx, func() error {
		n = o.entries().DeleteWhere(func(k outboxKey, _ types.OutboxEntry) bool {
			return k.origin == origin && k.stream == stream && k.seq <= upTo
		})
		return nil
	})
	return n, err
}

func (o outboxStore) PutCursor(ctx context.Context, c types.PeerCursor) error {
	return o.s.Do(ctx, func() error {
		o.cursors().Put(peersPair{c.PeerInstanceID, c.Stream}, c)
		return nil
	})
}

func (o outboxStore) Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error) {
	var out types.PeerCursor
	err := o.s.Do(ctx, func() error {
		var ok bool
		if out, ok = o.cursors().Get(peersPair{peer, stream}); !ok {
			return memtx.NotFound("peer cursor", peer+" "+stream)
		}
		return nil
	})
	return out, err
}

func (o outboxStore) Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error) {
	var out []types.PeerCursor
	err := o.s.Do(ctx, func() error {
		out = o.cursors().All(func(k peersPair, _ types.PeerCursor) bool {
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
	return o.s.Do(ctx, func() error {
		o.cursors().DeleteWhere(func(k peersPair, _ types.PeerCursor) bool { return k.a == peer })
		return nil
	})
}

func (o outboxStore) Applied(
	ctx context.Context,
	origin, stream string,
) (types.AppliedMarker, error) {
	out := types.AppliedMarker{Origin: origin, Stream: stream}
	err := o.s.Do(ctx, func() error {
		if m, ok := o.applied().Get(peersPair{origin, stream}); ok {
			out = m
		}
		return nil
	})
	return out, err
}

func (o outboxStore) SetApplied(ctx context.Context, m types.AppliedMarker) error {
	return o.s.Do(ctx, func() error {
		o.applied().Put(peersPair{m.Origin, m.Stream}, m)
		return nil
	})
}

func (o outboxStore) RecordFailedFetch(
	ctx context.Context,
	f types.FailedBlobFetch,
) (types.FailedBlobFetch, error) {
	var out types.FailedBlobFetch
	err := o.s.Do(ctx, func() error {
		k := peersPair{f.Stream, f.SHA256}
		cur, ok := o.failed().Get(k)
		if ok {
			cur.Failures++
			cur.LastError = f.LastError
		} else {
			cur = f
			cur.Failures = 1
		}
		o.failed().Put(k, cur)
		out, _ = o.failed().Get(k)
		return nil
	})
	return out, err
}

func (o outboxStore) FailedFetches(
	ctx context.Context,
	stream string,
) ([]types.FailedBlobFetch, error) {
	var out []types.FailedBlobFetch
	err := o.s.Do(ctx, func() error {
		out = o.failed().All(func(k peersPair, _ types.FailedBlobFetch) bool {
			return stream == "" || k.a == stream
		}, func(a, b types.FailedBlobFetch) int {
			return cmp.Or(strings.Compare(a.Stream, b.Stream), strings.Compare(a.SHA256, b.SHA256))
		})
		return nil
	})
	return out, err
}

func (o outboxStore) ClearFailedFetch(ctx context.Context, stream, sha256 string) error {
	return o.s.Do(ctx, func() error {
		if !o.failed().Del(peersPair{stream, sha256}) {
			return memtx.NotFound("failed blob fetch", stream+" "+sha256)
		}
		return nil
	})
}
