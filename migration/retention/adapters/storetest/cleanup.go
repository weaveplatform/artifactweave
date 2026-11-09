package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/migration/retention/api"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
	"github.com/weaveplatform/artifactweave/migration/retention/core/port"
)

// runCleanup covers cleanup policies and runs (decision 0010).
func runCleanup(t *testing.T, newStore Factory) {
	t.Run("CleanupPolicies", func(t *testing.T) { testCleanupPolicies(t, newStore(t)) })
	t.Run("CleanupRuns", func(t *testing.T) { testCleanupRuns(t, newStore(t)) })
}

func cleanupPolicy(key string, item model.CleanupItemType, project string) model.CleanupPolicy {
	return model.CleanupPolicy{
		Key:             key,
		Description:     "policy " + key,
		CronExp:         "0 0 2 * * ?",
		ProjectKey:      project,
		ItemType:        item,
		DurationMinutes: 60,
		Enabled:         true,
		SkipTrashcan:    true,
		Criteria: model.CleanupCriteria{
			PackageTypes: []string{
				"npm",
			},
			Repos:               []string{"npm-local"},
			IncludedPackages:    []string{"**"},
			CreatedBeforeInDays: 30,
			KeepLastNVersions:   3,
			IncludedProperties:  map[string][]string{"stage": {"dev", "test"}},
		},
		Created:   storekit.At(0),
		CreatedBy: "admin",
		Updated:   storekit.At(1),
		UpdatedBy: "ann",
	}
}

func testCleanupPolicies(t *testing.T, s port.Store) {
	ctx := context.Background()
	cs := s.Cleanup()
	k1 := cleanupPolicy("k1", model.CleanupPackage, "p1")
	k2 := cleanupPolicy("k2", model.CleanupBuild, "")
	k3 := cleanupPolicy("k3", model.CleanupPackage, "p1")
	for _, p := range []model.CleanupPolicy{k3, k2, k1} {
		storekit.Must(t, cs.CreatePolicy(ctx, p))
	}
	storekit.WantErr(t, cs.CreatePolicy(ctx, k1), kernel.ErrConflict)
	storekit.Same(t, "Policy", k1, storekit.Get[model.CleanupPolicy](t)(cs.Policy(ctx, "k1")))
	_, err := cs.Policy(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	policies := func(f port.PolicyFilter) []model.CleanupPolicy {
		return storekit.Get[[]model.CleanupPolicy](t)(cs.Policies(ctx, f))
	}
	storekit.Same(t, "all", []model.CleanupPolicy{k1, k2, k3}, policies(port.PolicyFilter{}))
	storekit.Same(
		t,
		"packages",
		[]model.CleanupPolicy{k1, k3},
		policies(port.PolicyFilter{ItemType: model.CleanupPackage}),
	)
	storekit.Same(
		t,
		"builds",
		[]model.CleanupPolicy{k2},
		policies(port.PolicyFilter{ItemType: model.CleanupBuild}),
	)
	storekit.Same(
		t,
		"project",
		[]model.CleanupPolicy{k1, k3},
		policies(port.PolicyFilter{ProjectKey: "p1"}),
	)

	got := storekit.Get[model.CleanupPolicy](t)(cs.Policy(ctx, "k1"))
	got.Criteria.IncludedProperties["stage"][0] = "mutated"
	storekit.Same(
		t,
		"Policy is a copy",
		k1,
		storekit.Get[model.CleanupPolicy](t)(cs.Policy(ctx, "k1")),
	)

	k1.Enabled, k1.Criteria, k1.Updated = false, model.CleanupCriteria{}, storekit.At(9)
	storekit.Must(t, cs.UpdatePolicy(ctx, k1))
	storekit.Same(t, "after Update", k1, storekit.Get[model.CleanupPolicy](t)(cs.Policy(ctx, "k1")))
	storekit.WantErr(
		t,
		cs.UpdatePolicy(ctx, cleanupPolicy("nope", model.CleanupPackage, "")),
		kernel.ErrNotFound,
	)

	storekit.Must(t, cs.DeletePolicy(ctx, "k1"))
	storekit.WantErr(t, cs.DeletePolicy(ctx, "k1"), kernel.ErrNotFound)
	storekit.Same(t, "after delete", []model.CleanupPolicy{k2, k3}, policies(port.PolicyFilter{}))
}

func testCleanupRuns(t *testing.T, s port.Store) {
	ctx := context.Background()
	cs := s.Cleanup()
	storekit.Must(t, cs.CreatePolicy(ctx, cleanupPolicy("a", model.CleanupPackage, "")))
	statuses := []api.RunStatus{
		api.RunCompleted,
		api.RunFailed,
		api.RunInProgress,
		api.RunCompleted,
		api.RunStopped,
	}
	runs := make([]model.CleanupRun, 0, len(statuses))
	for i, st := range statuses {
		r := model.CleanupRun{
			PolicyKey:   "a",
			TriggerID:   int64(100 + i),
			ItemType:    model.CleanupPackage,
			Status:      st,
			DryRun:      i%2 == 0,
			Manual:      i == 1,
			TriggeredBy: "scheduler",
			Started: storekit.At(
				i,
			),
			TotalItems:   int64(10 * i),
			CleanedItems: int64(i),
			CleanedSize:  int64(1000 * i),
			Report:       []byte(`{"rows":[]}`),
		}
		if i == 4 {
			r.PolicyKey, r.ItemType = "b", model.CleanupBuild
		}
		if st != api.RunInProgress {
			r.Ended = storekit.At(i).Add(30 * time.Second)
		}
		got := storekit.Get[model.CleanupRun](t)(cs.CreateRun(ctx, r))
		if i > 0 && got.ID <= runs[i-1].ID {
			t.Fatalf("run ID %d after %d", got.ID, runs[i-1].ID)
		}
		r.ID = got.ID
		storekit.Same(t, "CreateRun", r, got)
		runs = append(runs, r)
	}
	storekit.Same(t, "Run", runs[2], storekit.Get[model.CleanupRun](t)(cs.Run(ctx, runs[2].ID)))
	_, err := cs.Run(ctx, runs[4].ID+100)
	storekit.WantErr(t, err, kernel.ErrNotFound)

	runs[2].Status, runs[2].Ended, runs[2].Report = api.RunCompletedWithErrors, storekit.At(
		20,
	), nil
	storekit.Must(t, cs.UpdateRun(ctx, runs[2]))
	storekit.Same(
		t,
		"after UpdateRun",
		runs[2],
		storekit.Get[model.CleanupRun](t)(cs.Run(ctx, runs[2].ID)),
	)
	storekit.WantErr(
		t,
		cs.UpdateRun(ctx, model.CleanupRun{ID: runs[4].ID + 100}),
		kernel.ErrNotFound,
	)

	// Deleting the policy keeps its runs for the run views.
	storekit.Must(t, cs.DeletePolicy(ctx, "a"))
	list := func(f port.RunFilter) []model.CleanupRun {
		return storekit.Pages(
			t,
			2,
			func(p kernel.Page) ([]model.CleanupRun, string, error) { return cs.Runs(ctx, f, p) },
		)
	}
	storekit.Same(
		t,
		"newest first",
		[]model.CleanupRun{runs[4], runs[3], runs[2], runs[1], runs[0]},
		list(port.RunFilter{}),
	)
	storekit.Same(
		t,
		"by policy",
		[]model.CleanupRun{runs[3], runs[2], runs[1], runs[0]},
		list(port.RunFilter{PolicyKey: "a"}),
	)
	storekit.Same(
		t,
		"by item type",
		[]model.CleanupRun{runs[4]},
		list(port.RunFilter{ItemType: model.CleanupBuild}),
	)
	storekit.Same(t, "by status", []model.CleanupRun{runs[3], runs[1], runs[0]},
		list(port.RunFilter{Statuses: []api.RunStatus{api.RunCompleted, api.RunFailed}}))

	_, _, err = cs.Runs(ctx, port.RunFilter{}, kernel.Page{After: kernel.Cursor("x")})
	storekit.WantErr(t, err, kernel.ErrInvalidCursor)

	storekit.Same(t, "LastRun", runs[3], storekit.Get[model.CleanupRun](t)(cs.LastRun(ctx, "a")))
	_, err = cs.LastRun(ctx, "nope")
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
