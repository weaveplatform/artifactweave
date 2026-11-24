package sync_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

func TestUnitFullSyncRestartsWhenTheSnapshotGoes(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	b.engine.Batch = 2
	behind(t, w, a, b)
	a.put("after", "the prune")
	a.run(streamsync.SendKind)
	// A drops the snapshot B is part way through, as its idle prune would.
	dropped := false
	w.onSnapshot = func(cursor string) {
		if cursor == "" || dropped {
			return
		}
		dropped = true
		if _, err := a.store.Snapshots().PruneSnapshots(t.Context(), a.clock.Add(time.Hour)); err != nil {
			t.Error(err)
		}
	}
	b.run(streamsync.FullSyncKind)
	if !dropped {
		t.Fatal("the snapshot was read in one page")
	}
	converged(t, b, a)
	runs := b.runs()
	if len(runs) != 1 || runs[0].Status != retentionapi.RunCompleted {
		t.Fatalf("runs %+v, want one completed from a second snapshot", runs)
	}
}

func TestUnitFullSyncRestartsWhenTheStreamIsPrunedAgain(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPulled, model.DeliveryPull)
	for i := range 3 {
		a.put(fmt.Sprint("k", i), "first")
	}
	*a.clock = t0.Add(streamsync.DefaultRetention + time.Hour)
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The snapshot is served; as B pulls what follows it, A writes, the
	// window passes again and A prunes that too.
	// B's pull is answered 410, which starts the full sync.
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	w.onPull = func(after int64) {
		if after != 3 {
			return
		}
		w.onPull = nil
		a.put("between", "pruned before B pulls it")
		*a.clock = a.clock.Add(streamsync.DefaultRetention + time.Hour)
		a.put("fresh", "kept")
		if err := a.engine.Prune(t.Context()); err != nil {
			t.Error(err)
		}
	}
	b.run(streamsync.FullSyncKind)
	b.run(streamsync.SweepKind)
	b.run(streamsync.PullKind)
	converged(t, b, a)
	if runs := b.runs(); len(runs) != 1 || runs[0].Status != retentionapi.RunCompleted || w.snapshots < 2 {
		t.Fatalf("runs %+v after %d snapshot pages, want one completed from a second snapshot", runs, w.snapshots)
	}
}

func TestUnitDispatchCatchesUpAPeerAtTheFloor(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	for i := range 5 {
		a.put("k", fmt.Sprint(i))
	}
	a.run(streamsync.SendKind)
	// A forgot that B acknowledged 3 to 5, and they expire.
	c := must(a.store.Outbox().Cursor(t.Context(), b.id, stream))
	c.AckedSeq = 2
	if err := a.store.Outbox().PutCursor(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	*a.clock = t0.Add(streamsync.DefaultRetention + time.Hour)
	a.put("k", "6")
	if err := a.engine.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f := a.resyncs(); len(f) != 1 || f[0].Position != 2 {
		t.Fatalf("flags %+v, want B at 2", f)
	}
	// B takes the first entry A holds, since it is the one B expects: no
	// full sync, and A pushes on from there.
	a.run(streamsync.SendKind)
	wantApplied(t, b.applied(a), seqs(1, 6))
	if a.cursor(b) != 6 || len(a.resyncs()) != 0 || len(b.runs()) != 0 {
		t.Fatalf("cursor %d, flags %+v, runs %+v", a.cursor(b), a.resyncs(), b.runs())
	}
}

// badItems is the plain kind with a snapshot side that lists items.
type badItems struct {
	plain
	items *[]model.SnapshotEntry
}

func (k badItems) SnapshotItems(_ context.Context, _, _, after string, _ int) ([]model.SnapshotEntry, error) {
	if after != "" {
		return nil, nil
	}
	return *k.items, nil
}

func TestUnitSnapshotRefusesItemsTheWireCannotCarry(t *testing.T) {
	w := newWorld()
	a, b := w.add(t), w.add(t)
	pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
	var items []model.SnapshotEntry
	if err := a.engine.Kinds.Register(badItems{plain{a.kind}, &items}); err != nil {
		t.Fatal(err)
	}
	a.kind.Subscribe("plain:s", b.id)
	at := outbox.HLC{Wall: t0.UnixMilli()}
	ok := model.SnapshotEntry{Key: "k", Time: at, Origin: a.id, Payload: []byte(`{}`)}
	sha := strings.Repeat("a", 64)
	for name, en := range map[string]model.SnapshotEntry{
		"no key":             {Time: at, Origin: a.id},
		"no origin":          {Key: "k", Time: at},
		"payload not object": {Key: "k", Time: at, Origin: a.id, Payload: []byte(`[1]`)},
		"blob not hex":       {Key: "k", Time: at, Origin: a.id, BlobSHA256: "ABC"},
		"negative size":      {Key: "k", Time: at, Origin: a.id, BlobSHA256: sha, BlobSize: -1},
		"size without blob":  {Key: "k", Time: at, Origin: a.id, BlobSize: 3},
		"tombstone payload":  {Key: "k", Time: at, Origin: a.id, Deleted: true, Payload: []byte(`{}`)},
		"tombstone blob":     {Key: "k", Time: at, Origin: a.id, Deleted: true, BlobSHA256: sha},
	} {
		items = []model.SnapshotEntry{en}
		if _, err := a.engine.Snapshot(t.Context(), a.record(b.id), "plain:s", "", 10); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
	later := ok
	later.Key = "l"
	items = []model.SnapshotEntry{later, ok}
	if _, err := a.engine.Snapshot(t.Context(), a.record(b.id), "plain:s", "", 10); !errors.Is(err, kernel.ErrInvalid) {
		t.Errorf("keys out of order: %v, want invalid", err)
	}
	items = []model.SnapshotEntry{ok, later}
	if page, err := a.engine.Snapshot(t.Context(), a.record(b.id), "plain:s", "", 10); err != nil || len(page.Entries) != 2 {
		t.Fatalf("valid items: %+v, %v", page, err)
	}
}
