package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// runStreams covers what a stream's full sync keeps (decision 0021, Full
// sync): the sender's snapshots and the receiver's run.
func runStreams(t *testing.T, newStore Factory) {
	t.Run("StreamResyncRun", func(t *testing.T) { testStreamResyncRun(t, newStore(t)) })
	t.Run("FullSyncProgress", func(t *testing.T) { testFullSyncProgress(t, newStore(t)) })
	t.Run("Snapshots", func(t *testing.T) { testSnapshots(t, newStore(t)) })
	t.Run("SnapshotPrune", func(t *testing.T) { testSnapshotPrune(t, newStore(t)) })
}

// testStreamResyncRun: Get reads one flag, with the run resynchronising it.
func testStreamResyncRun(t *testing.T, s port.Store) {
	ctx := context.Background()
	rs := s.Resyncs()
	self, a := peerID(1), peerID(2)
	_, err := rs.Get(ctx, a, self, "test:s")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	f := model.StreamResync{Origin: a, Receiver: self, Stream: "test:s", Position: 4, Flagged: storekit.At(1)}
	storekit.Must(t, rs.Flag(ctx, f))
	storekit.Same(t, "Get before a run", f, storekit.Get[model.StreamResync](t)(rs.Get(ctx, a, self, "test:s")))
	f.Run = 7
	storekit.Must(t, rs.Flag(ctx, f))
	storekit.Same(t, "Get with its run", f, storekit.Get[model.StreamResync](t)(rs.Get(ctx, a, self, "test:s")))
	storekit.Same(t, "Resyncs with its run", []model.StreamResync{f},
		storekit.Get[[]model.StreamResync](t)(rs.Resyncs(ctx, self)))
	// The other direction is another flag.
	_, err = rs.Get(ctx, self, a, "test:s")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	failsCancelled(t, map[string]func(context.Context) error{
		"Get": func(ctx context.Context) error { _, err := rs.Get(ctx, a, self, "test:s"); return err }, //nolint:wrapcheck // the error is the subject
	})
}

// testFullSyncProgress: a run keeps its stream, phase and position across
// updates.
func testFullSyncProgress(t *testing.T, s port.Store) {
	ctx := context.Background()
	fs := s.Federation()
	r := storekit.Get[model.FullSyncRun](t)(fs.CreateFullSync(ctx, model.FullSyncRun{
		PeerInstanceID: peerID(1), Stream: "test:s", Phase: model.FullSyncSnapshot,
		Started: storekit.At(1), Status: retentionapi.RunInProgress,
	}))
	r.Cursor, r.LastKey, r.SnapshotSeq = "c2", "k/9", 12
	storekit.Must(t, fs.UpdateFullSync(ctx, r))
	storekit.Same(t, "a run part way through its snapshot", r,
		storekit.Get[model.FullSyncRun](t)(fs.FullSync(ctx, r.ID)))
	r.Phase, r.Cursor, r.LastKey = model.FullSyncStream, "", ""
	r.Status, r.Message, r.Finished = retentionapi.RunFailed, "the peer refused", storekit.At(2)
	storekit.Must(t, fs.UpdateFullSync(ctx, r))
	storekit.Same(t, "a stopped run", r, storekit.Get[model.FullSyncRun](t)(fs.FullSync(ctx, r.ID)))
}

func snapshotEntry(key string, n int) model.SnapshotEntry {
	return model.SnapshotEntry{
		Key: key, Time: outbox.HLC{Wall: storekit.At(n).UnixMilli(), Logical: uint32(n)}, // #nosec G115 -- small
		Origin: peerID(n), Payload: []byte(`{"n":12345678901234567891}`),
	}
}

// testSnapshots: a snapshot's entries are kept in byte order of key, a
// receiver's new snapshot of a stream replaces its earlier one, and reads
// are recorded.
func testSnapshots(t *testing.T, s port.Store) {
	ctx := context.Background()
	ss := s.Snapshots()
	_, err := ss.Snapshot(ctx, 1)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, ss.ReadSnapshot(ctx, 1, storekit.At(1)), kernel.ErrNotFound)

	a, b := peerID(1), peerID(2)
	first := model.StreamSnapshot{Stream: "test:s", Receiver: a, Sequence: 5, Taken: storekit.At(1), Read: storekit.At(1)}
	got := storekit.Get[model.StreamSnapshot](t)(ss.CreateSnapshot(ctx, first))
	if got.ID <= 0 {
		t.Fatalf("CreateSnapshot id = %d", got.ID)
	}
	first.ID = got.ID
	storekit.Same(t, "CreateSnapshot", first, got)
	storekit.Same(t, "Snapshot", first, storekit.Get[model.StreamSnapshot](t)(ss.Snapshot(ctx, first.ID)))

	// Byte order: "B" before "a", "a" before "a/b", which is before "ab".
	tomb := model.SnapshotEntry{Key: "a/b", Time: outbox.HLC{Wall: 7}, Origin: b, Deleted: true}
	content := snapshotEntry("ab", 3)
	content.BlobSHA256, content.BlobSize = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 9
	storekit.Must(t, ss.AddSnapshotEntries(ctx, first.ID, []model.SnapshotEntry{snapshotEntry("a", 1), content}))
	storekit.Must(t, ss.AddSnapshotEntries(ctx, first.ID, []model.SnapshotEntry{tomb, snapshotEntry("B", 2)}))
	all := []model.SnapshotEntry{snapshotEntry("B", 2), snapshotEntry("a", 1), tomb, content}
	storekit.Same(t, "SnapshotEntries", all,
		storekit.Get[[]model.SnapshotEntry](t)(ss.SnapshotEntries(ctx, first.ID, "", 10)))
	storekit.Same(t, "SnapshotEntries after a, two", all[2:4],
		storekit.Get[[]model.SnapshotEntry](t)(ss.SnapshotEntries(ctx, first.ID, "a", 2)))
	storekit.Same(t, "SnapshotEntries after the last", []model.SnapshotEntry(nil),
		storekit.Get[[]model.SnapshotEntry](t)(ss.SnapshotEntries(ctx, first.ID, "ab", 10)))
	err = ss.AddSnapshotEntries(ctx, first.ID, []model.SnapshotEntry{snapshotEntry("a", 4)})
	storekit.WantErr(t, err, kernel.ErrConflict)

	storekit.Must(t, ss.ReadSnapshot(ctx, first.ID, storekit.At(3)))
	first.Read = storekit.At(3)
	storekit.Same(t, "Snapshot after a read", first, storekit.Get[model.StreamSnapshot](t)(ss.Snapshot(ctx, first.ID)))

	// Another receiver's snapshot, and the same receiver's of another
	// stream, stay when a takes a new one of test:s.
	other := storekit.Get[model.StreamSnapshot](t)(ss.CreateSnapshot(ctx, model.StreamSnapshot{
		Stream: "test:s", Receiver: b, Sequence: 6, Taken: storekit.At(4),
	}))
	elsewhere := storekit.Get[model.StreamSnapshot](t)(ss.CreateSnapshot(ctx, model.StreamSnapshot{
		Stream: "test:t", Receiver: a, Sequence: 1, Taken: storekit.At(4),
	}))
	storekit.Must(t, ss.AddSnapshotEntries(ctx, other.ID, []model.SnapshotEntry{snapshotEntry("a", 5)}))
	again := storekit.Get[model.StreamSnapshot](t)(ss.CreateSnapshot(ctx, model.StreamSnapshot{
		Stream: "test:s", Receiver: a, Sequence: 9, Taken: storekit.At(5),
	}))
	if again.ID <= elsewhere.ID {
		t.Fatalf("CreateSnapshot id = %d after %d", again.ID, elsewhere.ID)
	}
	_, err = ss.Snapshot(ctx, first.ID)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(t, "the replaced snapshot's entries", []model.SnapshotEntry(nil),
		storekit.Get[[]model.SnapshotEntry](t)(ss.SnapshotEntries(ctx, first.ID, "", 10)))
	storekit.Same(t, "another receiver's snapshot", other, storekit.Get[model.StreamSnapshot](t)(ss.Snapshot(ctx, other.ID)))
	storekit.Same(t, "another stream's snapshot", elsewhere,
		storekit.Get[model.StreamSnapshot](t)(ss.Snapshot(ctx, elsewhere.ID)))
	storekit.Same(t, "another receiver's entries", []model.SnapshotEntry{snapshotEntry("a", 5)},
		storekit.Get[[]model.SnapshotEntry](t)(ss.SnapshotEntries(ctx, other.ID, "", 10)))
	// A new snapshot may hold the keys a replaced one did.
	storekit.Must(t, ss.AddSnapshotEntries(ctx, again.ID, []model.SnapshotEntry{snapshotEntry("a", 6)}))

	failsCancelled(t, map[string]func(context.Context) error{
		"CreateSnapshot": func(ctx context.Context) error { _, err := ss.CreateSnapshot(ctx, first); return err }, //nolint:wrapcheck // the error is the subject
		"Snapshot":       func(ctx context.Context) error { _, err := ss.Snapshot(ctx, other.ID); return err },    //nolint:wrapcheck // the error is the subject
		"AddSnapshotEntries": func(ctx context.Context) error {
			return ss.AddSnapshotEntries(ctx, other.ID, []model.SnapshotEntry{snapshotEntry("z", 1)})
		},
		"SnapshotEntries": func(ctx context.Context) error { _, err := ss.SnapshotEntries(ctx, other.ID, "", 1); return err }, //nolint:wrapcheck // the error is the subject
		"ReadSnapshot":    func(ctx context.Context) error { return ss.ReadSnapshot(ctx, other.ID, storekit.At(9)) },
		"PruneSnapshots":  func(ctx context.Context) error { _, err := ss.PruneSnapshots(ctx, storekit.At(9)); return err }, //nolint:wrapcheck // the error is the subject
	})
}

// testSnapshotPrune: a snapshot goes when it was last read, or taken and
// never read, before the cut, with its entries.
func testSnapshotPrune(t *testing.T, s port.Store) {
	ctx := context.Background()
	ss := s.Snapshots()
	take := func(receiver, taken, read int) model.StreamSnapshot {
		sn := model.StreamSnapshot{Stream: "test:s", Receiver: peerID(receiver), Taken: storekit.At(taken)}
		if read > 0 {
			sn.Read = storekit.At(read)
		}
		sn = storekit.Get[model.StreamSnapshot](t)(ss.CreateSnapshot(ctx, sn))
		storekit.Must(t, ss.AddSnapshotEntries(ctx, sn.ID, []model.SnapshotEntry{snapshotEntry("k", receiver)}))
		return sn
	}
	idle := take(1, 1, 2)    // last read at 2
	unread := take(2, 3, 0)  // taken at 3, never read
	reading := take(3, 1, 6) // taken early, read since
	fresh := take(4, 6, 0)   // taken at 6
	// Stores keep microseconds, so a cut at the instant idle was read
	// would fall after its stored time: the cut before any is At(1).
	storekit.Same(t, "PruneSnapshots of none", int64(0),
		storekit.Get[int64](t)(ss.PruneSnapshots(ctx, storekit.At(1))))
	storekit.Same(t, "PruneSnapshots", int64(2),
		storekit.Get[int64](t)(ss.PruneSnapshots(ctx, storekit.At(5))))
	for _, gone := range []model.StreamSnapshot{idle, unread} {
		_, err := ss.Snapshot(ctx, gone.ID)
		storekit.WantErr(t, err, kernel.ErrNotFound)
		storekit.Same(t, "a pruned snapshot's entries", []model.SnapshotEntry(nil),
			storekit.Get[[]model.SnapshotEntry](t)(ss.SnapshotEntries(ctx, gone.ID, "", 10)))
	}
	for _, kept := range []model.StreamSnapshot{reading, fresh} {
		storekit.Same(t, "a kept snapshot", kept, storekit.Get[model.StreamSnapshot](t)(ss.Snapshot(ctx, kept.ID)))
	}
}
