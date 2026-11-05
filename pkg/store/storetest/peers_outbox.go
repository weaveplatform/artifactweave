package storetest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runOutbox covers the outbox, peer cursors, applied markers and failed blob
// fetches (decision 0021).
func runOutbox(t *testing.T, newStore Factory) {
	t.Run("Outbox", func(t *testing.T) { testOutbox(t, newStore(t)) })
	t.Run("OutboxConcurrent", func(t *testing.T) { testOutboxConcurrent(t, newStore(t)) })
	t.Run("PeerCursors", func(t *testing.T) { testPeerCursors(t, newStore(t)) })
	t.Run("AppliedMarkers", func(t *testing.T) { testAppliedMarkers(t, newStore(t)) })
	t.Run("FailedFetches", func(t *testing.T) { testFailedFetches(t, newStore(t)) })
}

// entry builds an outbox entry; Seq is left for Append to assign.
func entry(origin, stream string, i int) types.OutboxEntry {
	return types.OutboxEntry{
		Origin: origin,
		Stream: stream,
		Time: types.HLC{
			Wall:    at(i).UnixMilli(),
			Logical: 4_000_000_000 + uint32(i),
		}, // #nosec G115 -- small fixture index
		EventType:  "deployed",
		Payload:    []byte(fmt.Sprintf(`{"path":"a/%d.jar"}`, i)),
		BlobSHA256: fmt.Sprintf("%064x", i),
		BlobSize:   int64(i * 10),
		Created:    at(i),
	}
}

// seqs returns the sequence numbers of entries.
func seqs(entries []types.OutboxEntry) []int64 {
	out := make([]int64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Seq)
	}
	return out
}

func testOutbox(t *testing.T, s store.Store) {
	ctx := context.Background()
	ob := s.Outbox()
	origin, other := peerID(1), peerID(2)
	repoA, repoB := types.RepositoryStream("a"), types.RepositoryStream("b")

	if seq := get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 0 {
		t.Fatalf("LastSeq of an empty stream = %d", seq)
	}
	e1 := entry(origin, repoA, 1)
	got := get[types.OutboxEntry](t)(ob.Append(ctx, e1))
	e1.Seq = 1
	same(t, "Append", e1, got)
	e2 := entry(origin, repoA, 2)
	e2.Payload = nil
	if got := get[types.OutboxEntry](t)(ob.Append(ctx, e2)); got.Seq != 2 {
		t.Fatalf("second Append seq = %d", got.Seq)
	}
	e2.Seq = 2
	// Streams number independently, per origin.
	for _, e := range []types.OutboxEntry{entry(origin, repoB, 3), entry(other, repoA, 4), entry(origin, types.AccessStream, 5)} {
		if got := get[types.OutboxEntry](t)(ob.Append(ctx, e)); got.Seq != 1 {
			t.Fatalf("first Append to %s/%s seq = %d", e.Origin, e.Stream, got.Seq)
		}
	}

	read := get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 0))
	same(t, "Read", []types.OutboxEntry{e1, e2}, read)
	read[0].Payload[0] = 'X'
	same(t, "Read after mutating a copy", []types.OutboxEntry{e1, e2},
		get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 0)))
	same(
		t,
		"Read after 1",
		[]types.OutboxEntry{e2},
		get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 1, 0)),
	)
	same(
		t,
		"Read limit 1",
		[]types.OutboxEntry{e1},
		get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 1)),
	)
	same(t, "Read past the end", []types.OutboxEntry(nil),
		get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 2, 0)))

	same(
		t,
		"Streams",
		[]string{types.AccessStream, repoA, repoB},
		get[[]string](t)(ob.Streams(ctx, origin)),
	)
	same(
		t,
		"Streams of an unknown origin",
		[]string(nil),
		get[[]string](t)(ob.Streams(ctx, peerID(9))),
	)

	// Pruning removes entries but never resets numbering.
	if n := get[int64](t)(ob.Prune(ctx, origin, repoA, 1)); n != 1 {
		t.Fatalf("Prune = %d", n)
	}
	same(
		t,
		"Read after Prune",
		[]int64{2},
		seqs(get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 0))),
	)
	if got := get[types.OutboxEntry](t)(ob.Append(ctx, entry(origin, repoA, 6))); got.Seq != 3 {
		t.Fatalf("Append after Prune seq = %d, want 3", got.Seq)
	}
	if n := get[int64](t)(ob.Prune(ctx, origin, repoA, 100)); n != 2 {
		t.Fatalf("Prune all = %d", n)
	}
	if seq := get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 3 {
		t.Fatalf("LastSeq after pruning everything = %d, want 3", seq)
	}
	same(
		t,
		"Streams after pruning",
		[]string{types.AccessStream, repoA, repoB},
		get[[]string](t)(ob.Streams(ctx, origin)),
	)

	// An append rolled back with its transaction gives its number back.
	err := s.Tx(ctx, func(ctx context.Context) error {
		got, err := ob.Append(ctx, entry(origin, repoA, 7))
		if err != nil {
			return err
		}
		if got.Seq != 4 {
			t.Errorf("Append in Tx seq = %d, want 4", got.Seq)
		}
		return errBoom
	})
	wantErr(t, err, errBoom)
	if seq := get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 3 {
		t.Fatalf("LastSeq after rollback = %d, want 3", seq)
	}
	same(
		t,
		"Read after rollback",
		[]types.OutboxEntry(nil),
		get[[]types.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 3, 0)),
	)
	if got := get[types.OutboxEntry](t)(ob.Append(ctx, entry(origin, repoA, 8))); got.Seq != 4 {
		t.Fatalf("Append after rollback seq = %d, want 4", got.Seq)
	}
	failsCancelled(t, map[string]func(context.Context) error{
		"Append":  func(ctx context.Context) error { _, err := ob.Append(ctx, entry(origin, repoA, 9)); return err },
		"Read":    func(ctx context.Context) error { _, err := ob.Read(ctx, origin, repoA, 0, 0); return err },
		"LastSeq": func(ctx context.Context) error { _, err := ob.LastSeq(ctx, origin, repoA); return err },
		"Streams": func(ctx context.Context) error { _, err := ob.Streams(ctx, origin); return err },
		"Prune":   func(ctx context.Context) error { _, err := ob.Prune(ctx, origin, repoA, 1); return err },
	})
	if seq := get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 4 {
		t.Fatalf("LastSeq after cancelled calls = %d, want 4", seq)
	}
}

func testOutboxConcurrent(t *testing.T, s store.Store) {
	ctx := context.Background()
	const workers, each = 8, 10
	origin, stream := peerID(1), types.RepositoryStream("hot")
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []int64
	)
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				e, err := s.Outbox().Append(ctx, entry(origin, stream, w*each+i))
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				got = append(got, e.Seq)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	slices.Sort(got)
	wantSeqs := make([]int64, 0, workers*each)
	for i := range workers * each {
		wantSeqs = append(wantSeqs, int64(i+1))
	}
	same(t, "concurrent sequence numbers", wantSeqs, got)
	read := get[[]types.OutboxEntry](t)(s.Outbox().Read(ctx, origin, stream, 0, 1000))
	same(t, "Read after concurrent appends", wantSeqs, seqs(read))
}

func testPeerCursors(t *testing.T, s store.Store) {
	ctx := context.Background()
	ob := s.Outbox()
	_, err := ob.Cursor(ctx, peerID(1), "repo:a")
	wantErr(t, err, store.ErrNotFound)
	c := func(peer int, stream string, acked int64) types.PeerCursor {
		return types.PeerCursor{
			PeerInstanceID: peerID(peer), Stream: stream, AckedSeq: acked, LastSent: at(int(acked)),
		}
	}
	p2a, p1b, p1a := c(2, "repo:a", 4), c(1, "repo:b", 2), c(1, "repo:a", 3)
	for _, v := range []types.PeerCursor{p2a, p1b, p1a} {
		must(t, ob.PutCursor(ctx, v))
	}
	same(t, "Cursor", p1a, get[types.PeerCursor](t)(ob.Cursor(ctx, peerID(1), "repo:a")))
	same(
		t,
		"Cursors",
		[]types.PeerCursor{p1a, p1b, p2a},
		get[[]types.PeerCursor](t)(ob.Cursors(ctx, "")),
	)
	same(
		t,
		"Cursors of one peer",
		[]types.PeerCursor{p1a, p1b},
		get[[]types.PeerCursor](t)(ob.Cursors(ctx, peerID(1))),
	)

	p1a.AckedSeq, p1a.LastSent, p1a.LastError = 9, at(9), "connection refused"
	must(t, ob.PutCursor(ctx, p1a))
	same(
		t,
		"Cursor after replace",
		p1a,
		get[types.PeerCursor](t)(ob.Cursor(ctx, peerID(1), "repo:a")),
	)

	must(t, ob.DeleteCursors(ctx, peerID(1)))
	same(
		t,
		"Cursors after delete",
		[]types.PeerCursor{p2a},
		get[[]types.PeerCursor](t)(ob.Cursors(ctx, "")),
	)
	must(t, ob.DeleteCursors(ctx, peerID(7)))
	same(
		t,
		"Cursors of a peer with none",
		[]types.PeerCursor(nil),
		get[[]types.PeerCursor](t)(ob.Cursors(ctx, peerID(7))),
	)
}

func testAppliedMarkers(t *testing.T, s store.Store) {
	ctx := context.Background()
	ob := s.Outbox()
	origin := peerID(1)
	zero := types.AppliedMarker{Origin: origin, Stream: "repo:a"}
	same(
		t,
		"Applied before any",
		zero,
		get[types.AppliedMarker](t)(ob.Applied(ctx, origin, "repo:a")),
	)
	for _, seq := range []int64{5, 7} {
		m := types.AppliedMarker{Origin: origin, Stream: "repo:a", LastSeq: seq}
		must(t, ob.SetApplied(ctx, m))
		same(t, "Applied", m, get[types.AppliedMarker](t)(ob.Applied(ctx, origin, "repo:a")))
	}
	other := types.AppliedMarker{Origin: peerID(2), Stream: "repo:a", LastSeq: 1}
	must(t, ob.SetApplied(ctx, other))
	same(
		t,
		"Applied of another origin",
		other,
		get[types.AppliedMarker](t)(ob.Applied(ctx, peerID(2), "repo:a")),
	)
	if m := get[types.AppliedMarker](t)(ob.Applied(ctx, origin, "repo:a")); m.LastSeq != 7 {
		t.Fatalf("Applied after another origin's update = %d", m.LastSeq)
	}
}

func testFailedFetches(t *testing.T, s store.Store) {
	ctx := context.Background()
	ob := s.Outbox()
	f := types.FailedBlobFetch{
		Stream: "repo:b", SHA256: fmt.Sprintf("%064x", 2), SHA1: fmt.Sprintf("%040x", 2),
		RepoKey: "b", FirstFailed: at(1), Failures: 9, LastError: "timeout",
	}
	got := get[types.FailedBlobFetch](t)(ob.RecordFailedFetch(ctx, f))
	first := f
	first.Failures = 1
	same(t, "first RecordFailedFetch", first, got)

	retry := f
	retry.FirstFailed, retry.LastError, retry.SHA1 = at(5), "404", "ignored"
	got = get[types.FailedBlobFetch](t)(ob.RecordFailedFetch(ctx, retry))
	second := first
	second.Failures, second.LastError = 2, "404"
	same(t, "second RecordFailedFetch", second, got)

	a := types.FailedBlobFetch{
		Stream:      "repo:a",
		SHA256:      fmt.Sprintf("%064x", 9),
		FirstFailed: at(2),
		LastError:   "x",
	}
	b2 := types.FailedBlobFetch{
		Stream:      "repo:b",
		SHA256:      fmt.Sprintf("%064x", 1),
		FirstFailed: at(3),
	}
	for _, v := range []types.FailedBlobFetch{a, b2} {
		get[types.FailedBlobFetch](t)(ob.RecordFailedFetch(ctx, v))
	}
	a.Failures, b2.Failures = 1, 1
	same(t, "FailedFetches", []types.FailedBlobFetch{a, b2, second},
		get[[]types.FailedBlobFetch](t)(ob.FailedFetches(ctx, "")))
	same(t, "FailedFetches of a stream", []types.FailedBlobFetch{b2, second},
		get[[]types.FailedBlobFetch](t)(ob.FailedFetches(ctx, "repo:b")))

	must(t, ob.ClearFailedFetch(ctx, "repo:b", f.SHA256))
	wantErr(t, ob.ClearFailedFetch(ctx, "repo:b", f.SHA256), store.ErrNotFound)
	same(t, "FailedFetches after clear", []types.FailedBlobFetch{b2},
		get[[]types.FailedBlobFetch](t)(ob.FailedFetches(ctx, "repo:b")))
	failsCancelled(t, map[string]func(context.Context) error{
		"RecordFailedFetch": func(ctx context.Context) error { _, err := ob.RecordFailedFetch(ctx, a); return err },
		"FailedFetches":     func(ctx context.Context) error { _, err := ob.FailedFetches(ctx, ""); return err },
		"ClearFailedFetch":  func(ctx context.Context) error { return ob.ClearFailedFetch(ctx, a.Stream, a.SHA256) },
	})
}
