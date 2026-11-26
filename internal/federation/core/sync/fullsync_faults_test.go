package sync_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	retentionapi "github.com/weaveplatform/artifactweave/internal/retention/api"
)

// The full sync's own calls, failing where f says, as faults_test.go's
// wrappers do for delivery's.

func (k faultKind) SnapshotItems(ctx context.Context, self, s, after string, limit int) ([]model.SnapshotEntry, error) {
	if err := k.f.check("Kind.SnapshotItems"); err != nil {
		return nil, err
	}
	return k.Kind.SnapshotItems(ctx, self, s, after, limit) //nolint:wrapcheck // passed through
}

func (k faultKind) LocalItems(ctx context.Context, origin, s, after string, limit int) ([]model.SnapshotEntry, error) {
	if err := k.f.check("Kind.LocalItems"); err != nil {
		return nil, err
	}
	return k.Kind.LocalItems(ctx, origin, s, after, limit) //nolint:wrapcheck // passed through
}

func (k faultKind) Reconcile(ctx context.Context, origin, s string, en model.SnapshotEntry, held bool) error {
	if err := k.f.check("Kind.Reconcile"); err != nil {
		return err
	}
	return k.Kind.Reconcile(ctx, origin, s, en, held) //nolint:wrapcheck // passed through
}

func (k faultKind) Discard(ctx context.Context, origin, s, key string) error {
	if err := k.f.check("Kind.Discard"); err != nil {
		return err
	}
	return k.Kind.Discard(ctx, origin, s, key) //nolint:wrapcheck // passed through
}

func (r faultResyncs) Get(ctx context.Context, origin, receiver, s string) (model.StreamResync, error) {
	if err := r.f.check("Resyncs.Get"); err != nil {
		return model.StreamResync{}, err
	}
	return r.StreamResyncStore.Get(ctx, origin, receiver, s) //nolint:wrapcheck // passed through
}

type faultSnapshots struct {
	port.SnapshotStore
	f *faultSet
}

func (n faultSnapshots) CreateSnapshot(ctx context.Context, v model.StreamSnapshot) (model.StreamSnapshot, error) {
	if err := n.f.check("Snapshots.CreateSnapshot"); err != nil {
		return model.StreamSnapshot{}, err
	}
	return n.SnapshotStore.CreateSnapshot(ctx, v) //nolint:wrapcheck // passed through
}

func (n faultSnapshots) Snapshot(ctx context.Context, id int64) (model.StreamSnapshot, error) {
	if err := n.f.check("Snapshots.Snapshot"); err != nil {
		return model.StreamSnapshot{}, err
	}
	return n.SnapshotStore.Snapshot(ctx, id) //nolint:wrapcheck // passed through
}

func (n faultSnapshots) AddSnapshotEntries(ctx context.Context, id int64, entries []model.SnapshotEntry) error {
	if err := n.f.check("Snapshots.AddSnapshotEntries"); err != nil {
		return err
	}
	return n.SnapshotStore.AddSnapshotEntries(ctx, id, entries) //nolint:wrapcheck // passed through
}

func (n faultSnapshots) SnapshotEntries(ctx context.Context, id int64, after string, limit int) ([]model.SnapshotEntry, error) {
	if err := n.f.check("Snapshots.SnapshotEntries"); err != nil {
		return nil, err
	}
	return n.SnapshotStore.SnapshotEntries(ctx, id, after, limit) //nolint:wrapcheck // passed through
}

func (n faultSnapshots) ReadSnapshot(ctx context.Context, id int64, at time.Time) error {
	if err := n.f.check("Snapshots.ReadSnapshot"); err != nil {
		return err
	}
	return n.SnapshotStore.ReadSnapshot(ctx, id, at) //nolint:wrapcheck // passed through
}

func (n faultSnapshots) PruneSnapshots(ctx context.Context, before time.Time) (int64, error) {
	if err := n.f.check("Snapshots.PruneSnapshots"); err != nil {
		return 0, err
	}
	return n.SnapshotStore.PruneSnapshots(ctx, before) //nolint:wrapcheck // passed through
}

type faultFederation struct {
	port.FederationStore
	f *faultSet
}

func (s faultFederation) CreateFullSync(ctx context.Context, r model.FullSyncRun) (model.FullSyncRun, error) {
	if err := s.f.check("Federation.CreateFullSync"); err != nil {
		return model.FullSyncRun{}, err
	}
	return s.FederationStore.CreateFullSync(ctx, r) //nolint:wrapcheck // passed through
}

func (s faultFederation) FullSync(ctx context.Context, id int64) (model.FullSyncRun, error) {
	if err := s.f.check("Federation.FullSync"); err != nil {
		return model.FullSyncRun{}, err
	}
	return s.FederationStore.FullSync(ctx, id) //nolint:wrapcheck // passed through
}

func (s faultFederation) UpdateFullSync(ctx context.Context, r model.FullSyncRun) error {
	if err := s.f.check("Federation.UpdateFullSync"); err != nil {
		return err
	}
	return s.FederationStore.UpdateFullSync(ctx, r) //nolint:wrapcheck // passed through
}

// TestUnitFullSyncFailuresSurface fails each call a full sync makes, in
// turn, on both sides, and checks that the failure reaches the caller, so
// the queue retries the work, and that the run then goes on to the end
// and converges, a page applied at most once.
func TestUnitFullSyncFailuresSurface(t *testing.T) {
	names := []string{
		"Kind.SnapshotItems", "Kind.LocalItems", "Kind.Reconcile", "Kind.Discard", "Kind.Apply",
		"Snapshots.CreateSnapshot", "Snapshots.Snapshot", "Snapshots.AddSnapshotEntries",
		"Snapshots.SnapshotEntries", "Snapshots.ReadSnapshot", "Snapshots.PruneSnapshots",
		"Federation.CreateFullSync", "Federation.FullSync", "Federation.UpdateFullSync",
		"Resyncs.Get", "Resyncs.Flag", "Resyncs.Clear",
		"Outbox.Read", "Outbox.LastSeq", "Outbox.Cursor", "Outbox.PutCursor", "Outbox.Applied",
		"Outbox.LockApplied", "Outbox.SetApplied", "Outbox.RecordFailedFetch",
		// Blobs.BeginUpload is left out: a fetch that cannot be stored is a
		// failed binary, which TestUnitFullSyncFailedFetchCompletesWithErrors
		// covers, not an error of the run.
		"Peers.Get", "Instance.ID", "Blobs.Stat",
	}
	for _, name := range names {
		for skip := range 4 {
			t.Run(fmt.Sprintf("%s-%d", name, skip), func(t *testing.T) {
				ctx := t.Context()
				w := newWorld()
				a, b := w.add(t), w.add(t)
				pair(t, a, b, fed, model.DeliveryPush, model.DeliveryPush)
				b.engine.Batch = 2
				behind(t, w, a, b)
				// Something only B holds, for the forced run to discard.
				if err := b.kind.Diverge(ctx, a.id, stream, "only-b", "x", outbox.HLC{Wall: t0.UnixMilli()}); err != nil {
					t.Fatal(err)
				}
				a.put("after", "the prune")

				w.inject.arm(name, skip)
				steps := []func() error{
					func() error { return a.engine.Dispatch(ctx, b.id, stream) },
					func() error { _, err := b.engine.FullSync(ctx, a.id, stream, true); return err },
				}
				for range 12 {
					steps = append(steps, func() error { return b.engine.Resync(ctx, a.id, stream) })
				}
				steps = append(steps,
					func() error { return b.engine.Sweep(ctx) },
					func() error { return a.engine.Prune(ctx) },
				)
				surfaced := false
				for _, step := range steps {
					if err := step(); errors.Is(err, errBoom) || err != nil && strings.Contains(err.Error(), errBoom.Error()) {
						surfaced = true
					}
				}
				if w.inject.hit() > 0 && !surfaced {
					t.Fatalf("%s failed %d calls and no operation reported it", name, w.inject.hit())
				}

				// Cleared: the forced run, asked for again, goes on to the end.
				w.inject.arm("", 0)
				if _, err := b.engine.FullSync(ctx, a.id, stream, true); err != nil {
					t.Fatal(err)
				}
				for range 12 {
					if err := b.engine.Resync(ctx, a.id, stream); err != nil {
						t.Fatal(err)
					}
				}
				for _, f := range b.failed() {
					if err := b.engine.RetryFetch(ctx, stream, f.SHA256); err != nil {
						t.Fatal(err)
					}
				}
				converged(t, b, a)
				runs := b.runs()
				if len(runs) == 0 || !runs[0].Status.Terminal() || runs[0].Status == retentionapi.RunFailed || !runs[0].Forced {
					t.Fatalf("runs %+v, want the forced one finished", runs)
				}
				if len(a.resyncs()) != 0 || len(b.resyncs()) != 0 {
					t.Fatalf("flags A %+v B %+v", a.resyncs(), b.resyncs())
				}
				a.put("later", "pushed")
				if err := a.engine.Dispatch(ctx, b.id, stream); err != nil {
					t.Fatal(err)
				}
				converged(t, b, a)
			})
		}
	}
}
