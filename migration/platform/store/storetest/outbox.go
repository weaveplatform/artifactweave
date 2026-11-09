package storetest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/outbox"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runOutbox covers the outbox, peer cursors, applied markers and failed blob
// fetches (decision 0021).
func runOutbox(t *testing.T, newStore func(*testing.T) Store) {
	t.Run("Outbox", func(t *testing.T) { testOutbox(t, newStore(t)) })
	t.Run("OutboxConcurrent", func(t *testing.T) { testOutboxConcurrent(t, newStore(t)) })
	t.Run("PeerCursors", func(t *testing.T) { testPeerCursors(t, newStore(t)) })
	t.Run("AppliedMarkers", func(t *testing.T) { testAppliedMarkers(t, newStore(t)) })
	t.Run("FailedFetches", func(t *testing.T) { testFailedFetches(t, newStore(t)) })
}

// repoStream and accessStream are the names the federation module gives a
// federated repository's stream and the Access stream. The outbox stores
// any stream name, and the platform may not import the module, so the
// suite spells them out.
func repoStream(repoKey string) string { return "repo:" + repoKey }

const accessStream = "access"

// entry builds an outbox entry; Seq is left for Append to assign.
func entry(origin, stream string, i int) outbox.OutboxEntry {
	return outbox.OutboxEntry{
		Origin: origin,
		Stream: stream,
		Time: outbox.HLC{
			Wall: storekit.At(i).UnixMilli(),
			//nolint:gosec // fixture indexes are small, so the sum fits
			Logical: 4_000_000_000 + uint32(i),
		},
		EventType:  "deployed",
		Payload:    []byte(fmt.Sprintf(`{"path":"a/%d.jar"}`, i)),
		BlobSHA256: fmt.Sprintf("%064x", i),
		BlobSize:   int64(i * 10),
		Created:    storekit.At(i),
	}
}

// seqs returns the sequence numbers of entries.
func seqs(entries []outbox.OutboxEntry) []int64 {
	out := make([]int64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Seq)
	}
	return out
}

func testOutbox(t *testing.T, s Store) {
	ctx := context.Background()
	ob := s.Outbox()
	origin, other := peerID(1), peerID(2)
	repoA, repoB := repoStream("a"), repoStream("b")

	if seq := storekit.Get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 0 {
		t.Fatalf("LastSeq of an empty stream = %d", seq)
	}
	e1 := entry(origin, repoA, 1)
	got := storekit.Get[outbox.OutboxEntry](t)(ob.Append(ctx, e1))
	e1.Seq = 1
	storekit.Same(t, "Append", e1, got)
	e2 := entry(origin, repoA, 2)
	e2.Payload = nil
	if got := storekit.Get[outbox.OutboxEntry](t)(ob.Append(ctx, e2)); got.Seq != 2 {
		t.Fatalf("second Append seq = %d", got.Seq)
	}
	e2.Seq = 2
	// Streams number independently, per origin.
	for _, e := range []outbox.OutboxEntry{entry(origin, repoB, 3), entry(other, repoA, 4), entry(origin, accessStream, 5)} {
		if got := storekit.Get[outbox.OutboxEntry](t)(ob.Append(ctx, e)); got.Seq != 1 {
			t.Fatalf("first Append to %s/%s seq = %d", e.Origin, e.Stream, got.Seq)
		}
	}

	read := storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 0))
	storekit.Same(t, "Read", []outbox.OutboxEntry{e1, e2}, read)
	read[0].Payload[0] = 'X'
	storekit.Same(t, "Read after mutating a copy", []outbox.OutboxEntry{e1, e2},
		storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 0)))
	storekit.Same(
		t,
		"Read after 1",
		[]outbox.OutboxEntry{e2},
		storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 1, 0)),
	)
	storekit.Same(
		t,
		"Read limit 1",
		[]outbox.OutboxEntry{e1},
		storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 1)),
	)
	storekit.Same(t, "Read past the end", []outbox.OutboxEntry(nil),
		storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 2, 0)))

	storekit.Same(
		t,
		"Streams",
		[]string{accessStream, repoA, repoB},
		storekit.Get[[]string](t)(ob.Streams(ctx, origin)),
	)
	storekit.Same(
		t,
		"Streams of an unknown origin",
		[]string(nil),
		storekit.Get[[]string](t)(ob.Streams(ctx, peerID(9))),
	)

	// Pruning removes entries but never resets numbering.
	if n := storekit.Get[int64](t)(ob.Prune(ctx, origin, repoA, 1)); n != 1 {
		t.Fatalf("Prune = %d", n)
	}
	storekit.Same(
		t,
		"Read after Prune",
		[]int64{2},
		seqs(storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 0, 0))),
	)
	if got := storekit.Get[outbox.OutboxEntry](
		t,
	)(
		ob.Append(ctx, entry(origin, repoA, 6)),
	); got.Seq != 3 {
		t.Fatalf("Append after Prune seq = %d, want 3", got.Seq)
	}
	if n := storekit.Get[int64](t)(ob.Prune(ctx, origin, repoA, 100)); n != 2 {
		t.Fatalf("Prune all = %d", n)
	}
	if seq := storekit.Get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 3 {
		t.Fatalf("LastSeq after pruning everything = %d, want 3", seq)
	}
	storekit.Same(
		t,
		"Streams after pruning",
		[]string{accessStream, repoA, repoB},
		storekit.Get[[]string](t)(ob.Streams(ctx, origin)),
	)

	// An append rolled back with its transaction gives its number back.
	err := s.Tx(ctx, func(ctx context.Context) error {
		got, err := ob.Append(ctx, entry(origin, repoA, 7))
		if err != nil {
			return err //nolint:wrapcheck // WantErr reports any error but errBoom
		}
		if got.Seq != 4 {
			t.Errorf("Append in Tx seq = %d, want 4", got.Seq)
		}
		return errBoom
	})
	storekit.WantErr(t, err, errBoom)
	if seq := storekit.Get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 3 {
		t.Fatalf("LastSeq after rollback = %d, want 3", seq)
	}
	storekit.Same(
		t,
		"Read after rollback",
		[]outbox.OutboxEntry(nil),
		storekit.Get[[]outbox.OutboxEntry](t)(ob.Read(ctx, origin, repoA, 3, 0)),
	)
	if got := storekit.Get[outbox.OutboxEntry](
		t,
	)(
		ob.Append(ctx, entry(origin, repoA, 8)),
	); got.Seq != 4 {
		t.Fatalf("Append after rollback seq = %d, want 4", got.Seq)
	}
	//nolint:wrapcheck // each case checks only that the call fails
	failsCancelled(t, map[string]func(context.Context) error{
		"Append":  func(ctx context.Context) error { _, err := ob.Append(ctx, entry(origin, repoA, 9)); return err },
		"Read":    func(ctx context.Context) error { _, err := ob.Read(ctx, origin, repoA, 0, 0); return err },
		"LastSeq": func(ctx context.Context) error { _, err := ob.LastSeq(ctx, origin, repoA); return err },
		"Streams": func(ctx context.Context) error { _, err := ob.Streams(ctx, origin); return err },
		"Prune":   func(ctx context.Context) error { _, err := ob.Prune(ctx, origin, repoA, 1); return err },
	})
	if seq := storekit.Get[int64](t)(ob.LastSeq(ctx, origin, repoA)); seq != 4 {
		t.Fatalf("LastSeq after cancelled calls = %d, want 4", seq)
	}
}

func testOutboxConcurrent(t *testing.T, s Store) {
	ctx := context.Background()
	const workers, each = 8, 10
	origin, stream := peerID(1), repoStream("hot")
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
	storekit.Same(t, "concurrent sequence numbers", wantSeqs, got)
	read := storekit.Get[[]outbox.OutboxEntry](t)(s.Outbox().Read(ctx, origin, stream, 0, 1000))
	storekit.Same(t, "Read after concurrent appends", wantSeqs, seqs(read))
}

func testPeerCursors(t *testing.T, s Store) {
	ctx := context.Background()
	ob := s.Outbox()
	_, err := ob.Cursor(ctx, peerID(1), "repo:a")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	c := func(peer int, stream string, acked int64) outbox.PeerCursor {
		return outbox.PeerCursor{
			PeerInstanceID: peerID(
				peer,
			),
			Stream:   stream,
			AckedSeq: acked,
			LastSent: storekit.At(int(acked)),
		}
	}
	p2a, p1b, p1a := c(2, "repo:a", 4), c(1, "repo:b", 2), c(1, "repo:a", 3)
	for _, v := range []outbox.PeerCursor{p2a, p1b, p1a} {
		storekit.Must(t, ob.PutCursor(ctx, v))
	}
	storekit.Same(
		t,
		"Cursor",
		p1a,
		storekit.Get[outbox.PeerCursor](t)(ob.Cursor(ctx, peerID(1), "repo:a")),
	)
	storekit.Same(
		t,
		"Cursors",
		[]outbox.PeerCursor{p1a, p1b, p2a},
		storekit.Get[[]outbox.PeerCursor](t)(ob.Cursors(ctx, "")),
	)
	storekit.Same(
		t,
		"Cursors of one peer",
		[]outbox.PeerCursor{p1a, p1b},
		storekit.Get[[]outbox.PeerCursor](t)(ob.Cursors(ctx, peerID(1))),
	)

	p1a.AckedSeq, p1a.LastSent, p1a.LastError = 9, storekit.At(9), "connection refused"
	storekit.Must(t, ob.PutCursor(ctx, p1a))
	storekit.Same(
		t,
		"Cursor after replace",
		p1a,
		storekit.Get[outbox.PeerCursor](t)(ob.Cursor(ctx, peerID(1), "repo:a")),
	)

	storekit.Must(t, ob.DeleteCursors(ctx, peerID(1)))
	storekit.Same(
		t,
		"Cursors after delete",
		[]outbox.PeerCursor{p2a},
		storekit.Get[[]outbox.PeerCursor](t)(ob.Cursors(ctx, "")),
	)
	storekit.Must(t, ob.DeleteCursors(ctx, peerID(7)))
	storekit.Same(
		t,
		"Cursors of a peer with none",
		[]outbox.PeerCursor(nil),
		storekit.Get[[]outbox.PeerCursor](t)(ob.Cursors(ctx, peerID(7))),
	)
}

func testAppliedMarkers(t *testing.T, s Store) {
	ctx := context.Background()
	ob := s.Outbox()
	origin := peerID(1)
	zero := outbox.AppliedMarker{Origin: origin, Stream: "repo:a"}
	storekit.Same(
		t,
		"Applied before any",
		zero,
		storekit.Get[outbox.AppliedMarker](t)(ob.Applied(ctx, origin, "repo:a")),
	)
	for _, seq := range []int64{5, 7} {
		m := outbox.AppliedMarker{Origin: origin, Stream: "repo:a", LastSeq: seq}
		storekit.Must(t, ob.SetApplied(ctx, m))
		storekit.Same(
			t,
			"Applied",
			m,
			storekit.Get[outbox.AppliedMarker](t)(ob.Applied(ctx, origin, "repo:a")),
		)
	}
	other := outbox.AppliedMarker{Origin: peerID(2), Stream: "repo:a", LastSeq: 1}
	storekit.Must(t, ob.SetApplied(ctx, other))
	storekit.Same(
		t,
		"Applied of another origin",
		other,
		storekit.Get[outbox.AppliedMarker](t)(ob.Applied(ctx, peerID(2), "repo:a")),
	)
	if m := storekit.Get[outbox.AppliedMarker](
		t,
	)(
		ob.Applied(ctx, origin, "repo:a"),
	); m.LastSeq != 7 {
		t.Fatalf("Applied after another origin's update = %d", m.LastSeq)
	}
}

func testFailedFetches(t *testing.T, s Store) {
	ctx := context.Background()
	ob := s.Outbox()
	f := outbox.FailedBlobFetch{
		Stream: "repo:b", SHA256: fmt.Sprintf("%064x", 2), SHA1: fmt.Sprintf("%040x", 2),
		RepoKey: "b", FirstFailed: storekit.At(1), Failures: 9, LastError: "timeout",
	}
	got := storekit.Get[outbox.FailedBlobFetch](t)(ob.RecordFailedFetch(ctx, f))
	first := f
	first.Failures = 1
	storekit.Same(t, "first RecordFailedFetch", first, got)

	retry := f
	retry.FirstFailed, retry.LastError, retry.SHA1 = storekit.At(5), "404", "ignored"
	got = storekit.Get[outbox.FailedBlobFetch](t)(ob.RecordFailedFetch(ctx, retry))
	second := first
	second.Failures, second.LastError = 2, "404"
	storekit.Same(t, "second RecordFailedFetch", second, got)

	a := outbox.FailedBlobFetch{
		Stream:      "repo:a",
		SHA256:      fmt.Sprintf("%064x", 9),
		FirstFailed: storekit.At(2),
		LastError:   "x",
	}
	b2 := outbox.FailedBlobFetch{
		Stream:      "repo:b",
		SHA256:      fmt.Sprintf("%064x", 1),
		FirstFailed: storekit.At(3),
	}
	for _, v := range []outbox.FailedBlobFetch{a, b2} {
		storekit.Get[outbox.FailedBlobFetch](t)(ob.RecordFailedFetch(ctx, v))
	}
	a.Failures, b2.Failures = 1, 1
	storekit.Same(t, "FailedFetches", []outbox.FailedBlobFetch{a, b2, second},
		storekit.Get[[]outbox.FailedBlobFetch](t)(ob.FailedFetches(ctx, "")))
	storekit.Same(t, "FailedFetches of a stream", []outbox.FailedBlobFetch{b2, second},
		storekit.Get[[]outbox.FailedBlobFetch](t)(ob.FailedFetches(ctx, "repo:b")))

	storekit.Must(t, ob.ClearFailedFetch(ctx, "repo:b", f.SHA256))
	storekit.WantErr(t, ob.ClearFailedFetch(ctx, "repo:b", f.SHA256), kernel.ErrNotFound)
	storekit.Same(t, "FailedFetches after clear", []outbox.FailedBlobFetch{b2},
		storekit.Get[[]outbox.FailedBlobFetch](t)(ob.FailedFetches(ctx, "repo:b")))
	//nolint:wrapcheck // each case checks only that the call fails
	failsCancelled(t, map[string]func(context.Context) error{
		"RecordFailedFetch": func(ctx context.Context) error { _, err := ob.RecordFailedFetch(ctx, a); return err },
		"FailedFetches":     func(ctx context.Context) error { _, err := ob.FailedFetches(ctx, ""); return err },
		"ClearFailedFetch":  func(ctx context.Context) error { return ob.ClearFailedFetch(ctx, a.Stream, a.SHA256) },
	})
}
