package storetest

import (
	"context"
	"fmt"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// The same continuation contract runs on memory, SQLite and PostgreSQL. In
// particular, an old worker cannot acknowledge a second effect after another
// worker has advanced the entry or finished the run.
func testCleanupProgress(t *testing.T, s port.Store) {
	ctx := t.Context()
	cs := s.Cleanup()
	r := newRunInTransaction(t, ctx, cs, "progress-policy")
	p := model.CleanupProgress{RunID: r.ID, Revision: 1, Phase: model.CleanupRepositories}
	write := func(fn func(context.Context) error) { t.Helper(); storekit.Must(t, s.Tx(ctx, fn)) }
	storekit.WantErr(t, cs.CreateProgress(ctx, p), kernel.ErrInvalid)
	write(func(ctx context.Context) error { return cs.CreateProgress(ctx, p) })
	storekit.Same(t, "initial continuation", p, storekit.Get[model.CleanupProgress](t)(cs.Progress(ctx, r.ID)))
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return cs.CreateProgress(ctx, p) }), kernel.ErrConflict)
	next := p
	next.Revision, next.Phase, next.Version.ID = 2, model.CleanupRemoving, 9
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return cs.AdvanceProgress(ctx, 0, next) }), kernel.ErrConflict)
	write(func(ctx context.Context) error { return cs.AdvanceProgress(ctx, p.Revision, next) })
	p = next
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return cs.AdvanceProgress(ctx, 1, next) }), kernel.ErrConflict)

	entries := []model.CleanupEntry{
		cleanupIntent(r.ID, 7, nil),
		cleanupIntent(r.ID, 3, &artifactsapi.IndexLead{Found: true, Rank: 20, TieBreak: "last"}),
		cleanupIntent(r.ID, 5, nil),
		cleanupIntent(r.ID, 2, &artifactsapi.IndexLead{Found: true, Rank: 10, TieBreak: "first"}),
	}
	for _, entry := range entries {
		write(func(ctx context.Context) error {
			created, err := cs.CreateEntry(ctx, entry)
			if err == nil && !created {
				t.Fatal("new intent was not created")
			}
			if err != nil {
				return fmt.Errorf("create selected intent: %w", err)
			}
			return nil
		})
	}
	write(func(ctx context.Context) error {
		created, err := cs.CreateEntry(ctx, entries[0])
		if err == nil && created {
			t.Fatal("replay duplicated an intent")
		}
		if err != nil {
			return fmt.Errorf("replay selected intent: %w", err)
		}
		return nil
	})
	copy := storekit.Get[model.CleanupEntry](t)(cs.Entry(ctx, r.ID, model.CleanupItemEntry, 7))
	copy.Snapshot.Properties["stage"][0] = "mutated outside store"
	storekit.Same(t, "intent is a detached snapshot", entries[0], storekit.Get[model.CleanupEntry](t)(cs.Entry(ctx, r.ID, model.CleanupItemEntry, 7)))
	changed := kernel.Clone(entries[0])
	changed.Snapshot.Path = "newer overwrite"
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error {
		_, err := cs.CreateEntry(ctx, changed)
		if err != nil {
			return fmt.Errorf("change selected intent: %w", err)
		}
		return nil
	}), kernel.ErrConflict)

	// A fault after both mutations must leave the original intent and cursor.
	rolled := entries[0]
	rolled.Revision, rolled.State, rolled.Reason = 2, model.CleanupSkipped, "rolled_back"
	err := s.Tx(ctx, func(ctx context.Context) error {
		if err := cs.AdvanceEntry(ctx, 1, rolled); err != nil {
			return fmt.Errorf("record rollback outcome: %w", err)
		}
		next := p
		next.Revision++
		next.Reason = "rolled_back"
		if err := cs.AdvanceProgress(ctx, p.Revision, next); err != nil {
			return fmt.Errorf("advance rollback continuation: %w", err)
		}
		return errExecutionRollback
	})
	storekit.WantErr(t, err, errExecutionRollback)
	storekit.Same(t, "effect acknowledgement rolled back", entries[0], storekit.Get[model.CleanupEntry](t)(cs.Entry(ctx, r.ID, model.CleanupItemEntry, 7)))
	storekit.Same(t, "continuation rolled back", p, storekit.Get[model.CleanupProgress](t)(cs.Progress(ctx, r.ID)))

	listed := storekit.Pages(t, 1, func(page kernel.Page) ([]model.CleanupEntry, string, error) {
		return cs.Entries(ctx, port.CleanupEntryQuery{RunID: r.ID, Kind: model.CleanupItemEntry}, page)
	})
	ids := make([]int64, 0, len(listed))
	for _, entry := range listed {
		ids = append(ids, entry.EntityID)
	}
	storekit.Same(t, "bounded stable enumeration", []int64{2, 3, 5, 7}, ids)
	for _, id := range []int64{5, 7, 2, 3} {
		entry := storekit.Get[model.CleanupEntry](t)(cs.PendingEntry(ctx, r.ID, 9))
		storekit.Same(t, "non-leads before ranked leads", id, entry.EntityID)
		entry.Revision++
		entry.State = model.CleanupDeleted
		entry.Removed = &artifactsapi.RemovedItem{ID: id, Path: entry.Snapshot.Path, Size: entry.Snapshot.Size}
		write(func(ctx context.Context) error { return cs.AdvanceEntry(ctx, 1, entry) })
		storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return cs.AdvanceEntry(ctx, 1, entry) }), kernel.ErrConflict)
	}
	_, err = cs.PendingEntry(ctx, r.ID, 9)
	storekit.WantErr(t, err, kernel.ErrNotFound)

	version := model.CleanupEntry{RunID: r.ID, EntityID: 9, VersionID: 9, Revision: 1, Kind: model.CleanupVersionEntry, State: model.CleanupSelected, Reason: "selected"}
	write(func(ctx context.Context) error {
		_, err := cs.CreateEntry(ctx, version)
		if err != nil {
			return fmt.Errorf("create version decision: %w", err)
		}
		return nil
	})
	next = p
	next.Revision++
	next.Phase = model.CleanupFinished
	write(func(ctx context.Context) error { return cs.AdvanceProgress(ctx, p.Revision, next) })
	late := cleanupIntent(r.ID, 20, nil)
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error {
		_, err := cs.CreateEntry(ctx, late)
		if err != nil {
			return fmt.Errorf("create late intent: %w", err)
		}
		return nil
	}), kernel.ErrConflict)
	version.Revision++
	version.State = model.CleanupSkipped
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return cs.AdvanceEntry(ctx, 1, version) }), kernel.ErrConflict)
	resurrected := next
	resurrected.Revision++
	resurrected.Phase = model.CleanupRemoving
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return cs.AdvanceProgress(ctx, next.Revision, resurrected) }), kernel.ErrConflict)
}

func cleanupIntent(run, id int64, lead *artifactsapi.IndexLead) model.CleanupEntry {
	return model.CleanupEntry{
		RunID:     run,
		EntityID:  id,
		VersionID: 9,
		Revision:  1,
		Kind:      model.CleanupItemEntry,
		State:     model.CleanupPending,
		Snapshot: artifactsapi.IndexFile{
			ID:         id,
			Version:    artifactsapi.IndexVersion{ID: 9},
			Path:       "package/file",
			Size:       12,
			Lead:       lead,
			Properties: map[string][]string{"stage": {"old"}},
		},
		Reason: "selected",
	}
}
