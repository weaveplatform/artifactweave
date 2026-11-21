package storetest

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func testCleanupOutcomeTotals(t *testing.T, store port.Store) {
	t.Helper()
	ctx, cs := t.Context(), store.Cleanup()
	newRun := func(t *testing.T) model.CleanupRun {
		t.Helper()
		r := storekit.Get[model.CleanupRun](t)(cs.CreateRun(ctx, model.CleanupRun{PolicyKey: "outcomes", ItemType: model.CleanupPackage, Status: api.RunInProgress}))
		storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error {
			return cs.CreateProgress(ctx, model.CleanupProgress{RunID: r.ID, Revision: 1, Phase: model.CleanupRepositories})
		}))
		return r
	}
	put := func(t *testing.T, run, id int64, state model.CleanupEntryState, size int64, hash string) {
		t.Helper()
		e := model.CleanupEntry{
			RunID: run, EntityID: id, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: state,
			Snapshot: artifactsapi.IndexFile{ID: id, Version: artifactsapi.IndexVersion{ID: 1}, Size: size, Actual: kernel.Checksums{SHA256: hash}},
		}
		if state == model.CleanupDeleted {
			e.Snapshot = artifactsapi.IndexFile{} // a real owner companion has only the effect facts
			e.Removed = &artifactsapi.RemovedItem{ID: id, Path: "companion.bin", Size: size, SHA256: hash}
		}
		storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error {
			if _, err := cs.CreateEntry(ctx, e); err != nil {
				return fmt.Errorf("seed cleanup report outcome: %w", err)
			}
			return nil
		}))
	}
	r := newRun(t)
	storekit.Same(t, "existing empty run", model.CleanupOutcomeTotals{}, storekit.Get[model.CleanupOutcomeTotals](t)(cs.OutcomeTotals(ctx, r.ID)))
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	put(t, r.ID, 1, model.CleanupDeleted, 10, a)
	put(t, r.ID, 2, model.CleanupDeleted, 10, a)
	put(t, r.ID, 3, model.CleanupDeleted, 7, b)
	put(t, r.ID, 4, model.CleanupSelected, 12, c)
	put(t, r.ID, 5, model.CleanupSelected, 12, c)
	put(t, r.ID, 6, model.CleanupSkipped, 9, a)
	put(t, r.ID, 7, model.CleanupEntryFailed, 5, b)
	put(t, r.ID, 8, model.CleanupPending, 4, c)
	put(t, r.ID, 9, model.CleanupDeleted, 3, "")
	put(t, r.ID, 10, model.CleanupDeleted, 3, "")
	put(t, r.ID, 11, model.CleanupSelected, 2, "")
	put(t, r.ID, 12, model.CleanupSelected, 2, "")
	storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error {
		_, err := cs.CreateEntry(ctx, model.CleanupEntry{RunID: r.ID, EntityID: 1, VersionID: 1, Revision: 1, Kind: model.CleanupVersionEntry, State: model.CleanupSelected})
		if err != nil {
			return fmt.Errorf("seed cleanup report version decision: %w", err)
		}
		return nil
	}))
	want := model.CleanupOutcomeTotals{
		Items:                12,
		Pending:              1,
		Selected:             4,
		Deleted:              5,
		Skipped:              1,
		Failed:               1,
		SelectedVirtualBytes: 28,
		SelectedContentBytes: 16,
		DeletedVirtualBytes:  33,
		DeletedContentBytes:  23,
	}
	storekit.Same(t, "truthful states, shared content and unknown checksums", want, storekit.Get[model.CleanupOutcomeTotals](t)(cs.OutcomeTotals(ctx, r.ID)))
	_, err := cs.OutcomeTotals(ctx, math.MaxInt64)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	for _, tc := range []struct {
		name                  string
		firstSize, secondSize int64
		firstHash, secondHash string
		state                 model.CleanupEntryState
	}{
		{name: "inconsistent shared content", firstSize: 10, secondSize: 11, firstHash: a, secondHash: a, state: model.CleanupDeleted},
		{name: "negative selected size", firstSize: -1, secondSize: 1, firstHash: a, secondHash: b, state: model.CleanupSelected},
		{name: "virtual size overflow", firstSize: math.MaxInt64, secondSize: math.MaxInt64, firstHash: a, secondHash: a, state: model.CleanupDeleted},
		{name: "unique content overflow", firstSize: math.MaxInt64, secondSize: math.MaxInt64, firstHash: a, secondHash: b, state: model.CleanupDeleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := newRun(t)
			put(t, bad.ID, 1, tc.state, tc.firstSize, tc.firstHash)
			put(t, bad.ID, 2, tc.state, tc.secondSize, tc.secondHash)
			totals, err := cs.OutcomeTotals(ctx, bad.ID)
			if err == nil {
				t.Fatal("invalid report data was accepted")
			}
			storekit.Same(t, "failed aggregation returns no partial totals", model.CleanupOutcomeTotals{}, totals)
		})
	}
	storekit.Same(t, "another run cannot poison this report", want, storekit.Get[model.CleanupOutcomeTotals](t)(cs.OutcomeTotals(ctx, r.ID)))
}
