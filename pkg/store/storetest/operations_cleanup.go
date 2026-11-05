package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runCleanup covers cleanup policies and runs (decision 0010).
func runCleanup(t *testing.T, newStore Factory) {
	t.Run("CleanupPolicies", func(t *testing.T) { testCleanupPolicies(t, newStore(t)) })
	t.Run("CleanupRuns", func(t *testing.T) { testCleanupRuns(t, newStore(t)) })
}

func cleanupPolicy(key string, item types.CleanupItemType, project string) types.CleanupPolicy {
	return types.CleanupPolicy{
		Key:             key,
		Description:     "policy " + key,
		CronExp:         "0 0 2 * * ?",
		ProjectKey:      project,
		ItemType:        item,
		DurationMinutes: 60,
		Enabled:         true,
		SkipTrashcan:    true,
		Criteria: types.CleanupCriteria{
			PackageTypes: []string{
				"npm",
			},
			Repos:               []string{"npm-local"},
			IncludedPackages:    []string{"**"},
			CreatedBeforeInDays: 30,
			KeepLastNVersions:   3,
			IncludedProperties:  map[string][]string{"stage": {"dev", "test"}},
		},
		Created:   at(0),
		CreatedBy: "admin",
		Updated:   at(1),
		UpdatedBy: "ann",
	}
}

func testCleanupPolicies(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Cleanup()
	k1 := cleanupPolicy("k1", types.CleanupPackage, "p1")
	k2 := cleanupPolicy("k2", types.CleanupBuild, "")
	k3 := cleanupPolicy("k3", types.CleanupPackage, "p1")
	for _, p := range []types.CleanupPolicy{k3, k2, k1} {
		must(t, cs.CreatePolicy(ctx, p))
	}
	wantErr(t, cs.CreatePolicy(ctx, k1), store.ErrConflict)
	same(t, "Policy", k1, get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1")))
	_, err := cs.Policy(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	policies := func(f store.PolicyFilter) []types.CleanupPolicy {
		return get[[]types.CleanupPolicy](t)(cs.Policies(ctx, f))
	}
	same(t, "all", []types.CleanupPolicy{k1, k2, k3}, policies(store.PolicyFilter{}))
	same(
		t,
		"packages",
		[]types.CleanupPolicy{k1, k3},
		policies(store.PolicyFilter{ItemType: types.CleanupPackage}),
	)
	same(
		t,
		"builds",
		[]types.CleanupPolicy{k2},
		policies(store.PolicyFilter{ItemType: types.CleanupBuild}),
	)
	same(
		t,
		"project",
		[]types.CleanupPolicy{k1, k3},
		policies(store.PolicyFilter{ProjectKey: "p1"}),
	)

	got := get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1"))
	got.Criteria.IncludedProperties["stage"][0] = "mutated"
	same(t, "Policy is a copy", k1, get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1")))

	k1.Enabled, k1.Criteria, k1.Updated = false, types.CleanupCriteria{}, at(9)
	must(t, cs.UpdatePolicy(ctx, k1))
	same(t, "after Update", k1, get[types.CleanupPolicy](t)(cs.Policy(ctx, "k1")))
	wantErr(
		t,
		cs.UpdatePolicy(ctx, cleanupPolicy("nope", types.CleanupPackage, "")),
		store.ErrNotFound,
	)

	must(t, cs.DeletePolicy(ctx, "k1"))
	wantErr(t, cs.DeletePolicy(ctx, "k1"), store.ErrNotFound)
	same(t, "after delete", []types.CleanupPolicy{k2, k3}, policies(store.PolicyFilter{}))
}

func testCleanupRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	cs := s.Cleanup()
	must(t, cs.CreatePolicy(ctx, cleanupPolicy("a", types.CleanupPackage, "")))
	statuses := []types.RunStatus{
		types.RunCompleted,
		types.RunFailed,
		types.RunInProgress,
		types.RunCompleted,
		types.RunStopped,
	}
	var runs []types.CleanupRun
	for i, st := range statuses {
		r := types.CleanupRun{
			PolicyKey:   "a",
			TriggerID:   int64(100 + i),
			ItemType:    types.CleanupPackage,
			Status:      st,
			DryRun:      i%2 == 0,
			Manual:      i == 1,
			TriggeredBy: "scheduler",
			Started: at(
				i,
			),
			TotalItems:   int64(10 * i),
			CleanedItems: int64(i),
			CleanedSize:  int64(1000 * i),
			Report:       []byte(`{"rows":[]}`),
		}
		if i == 4 {
			r.PolicyKey, r.ItemType = "b", types.CleanupBuild
		}
		if st != types.RunInProgress {
			r.Ended = at(i).Add(30 * time.Second)
		}
		got := get[types.CleanupRun](t)(cs.CreateRun(ctx, r))
		if i > 0 && got.ID <= runs[i-1].ID {
			t.Fatalf("run ID %d after %d", got.ID, runs[i-1].ID)
		}
		r.ID = got.ID
		same(t, "CreateRun", r, got)
		runs = append(runs, r)
	}
	same(t, "Run", runs[2], get[types.CleanupRun](t)(cs.Run(ctx, runs[2].ID)))
	_, err := cs.Run(ctx, runs[4].ID+100)
	wantErr(t, err, store.ErrNotFound)

	runs[2].Status, runs[2].Ended, runs[2].Report = types.RunCompletedWithErrors, at(20), nil
	must(t, cs.UpdateRun(ctx, runs[2]))
	same(t, "after UpdateRun", runs[2], get[types.CleanupRun](t)(cs.Run(ctx, runs[2].ID)))
	wantErr(t, cs.UpdateRun(ctx, types.CleanupRun{ID: runs[4].ID + 100}), store.ErrNotFound)

	// Deleting the policy keeps its runs for the run views.
	must(t, cs.DeletePolicy(ctx, "a"))
	list := func(f store.RunFilter) []types.CleanupRun {
		return pages(
			t,
			2,
			func(p store.Page) ([]types.CleanupRun, string, error) { return cs.Runs(ctx, f, p) },
		)
	}
	same(
		t,
		"newest first",
		[]types.CleanupRun{runs[4], runs[3], runs[2], runs[1], runs[0]},
		list(store.RunFilter{}),
	)
	same(
		t,
		"by policy",
		[]types.CleanupRun{runs[3], runs[2], runs[1], runs[0]},
		list(store.RunFilter{PolicyKey: "a"}),
	)
	same(
		t,
		"by item type",
		[]types.CleanupRun{runs[4]},
		list(store.RunFilter{ItemType: types.CleanupBuild}),
	)
	same(t, "by status", []types.CleanupRun{runs[3], runs[1], runs[0]},
		list(store.RunFilter{Statuses: []types.RunStatus{types.RunCompleted, types.RunFailed}}))

	_, _, err = cs.Runs(ctx, store.RunFilter{}, store.Page{After: store.Cursor("x")})
	wantErr(t, err, store.ErrInvalidCursor)

	same(t, "LastRun", runs[3], get[types.CleanupRun](t)(cs.LastRun(ctx, "a")))
	_, err = cs.LastRun(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)
}
