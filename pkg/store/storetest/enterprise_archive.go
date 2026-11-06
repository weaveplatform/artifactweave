package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runArchive covers archive policies and executions (decision 0024).
func runArchive(t *testing.T, newStore Factory) {
	t.Run("ArchivePolicies", func(t *testing.T) { testArchivePolicies(t, newStore(t)) })
	t.Run("ArchiveExecutions", func(t *testing.T) { testArchiveExecutions(t, newStore(t)) })
}

// fxPolicy builds an archive policy.
func fxPolicy(key string, family types.ArchiveFamily) types.ArchivePolicy {
	return types.ArchivePolicy{
		Key:              key,
		Family:           family,
		Description:      "archive " + key,
		Enabled:          true,
		CronExp:          "0 0 2 * * ?",
		ProjectKey:       "p1",
		DurationMinutes:  60,
		ExpirationMonths: 24,
		SkipTrashcan:     true,
		Criteria:         []byte(`{"repos":["libs-release"],"createdBeforeInMonths":12}`),
		Created:          at(1),
		Updated:          at(2),
	}
}

func testArchivePolicies(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	p := fxPolicy("old-jars", types.ArchiveRetentionV1)
	must(t, as.CreatePolicy(ctx, p))
	same(t, "Policy", p, get[types.ArchivePolicy](t)(as.Policy(ctx, "old-jars")))
	wantErr(t, as.CreatePolicy(ctx, p), store.ErrConflict)
	_, err := as.Policy(ctx, "nope")
	wantErr(t, err, store.ErrNotFound)

	p.Enabled, p.Criteria, p.Updated = false, nil, at(9)
	must(t, as.UpdatePolicy(ctx, p))
	same(t, "Policy after update", p, get[types.ArchivePolicy](t)(as.Policy(ctx, "old-jars")))
	wantErr(t, as.UpdatePolicy(ctx, fxPolicy("nope", types.ArchiveSmartV2)), store.ErrNotFound)

	must(t, as.CreatePolicy(ctx, fxPolicy("Smart", types.ArchiveSmartV2)))
	must(t, as.CreatePolicy(ctx, fxPolicy("docker", types.ArchiveSmartV2)))
	keys := func(f types.ArchiveFamily) []string {
		var out []string
		for _, v := range get[[]types.ArchivePolicy](t)(as.Policies(ctx, f)) {
			out = append(out, v.Key)
		}
		return out
	}
	same(t, "Policies", []string{"Smart", "docker", "old-jars"}, keys(""))
	same(t, "Policies(v2)", []string{"Smart", "docker"}, keys(types.ArchiveSmartV2))

	must(t, as.DeletePolicy(ctx, "docker"))
	_, err = as.Policy(ctx, "docker")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, as.DeletePolicy(ctx, "docker"), store.ErrNotFound)
}

func testArchiveExecutions(t *testing.T, s store.Store) {
	ctx := context.Background()
	as := s.Archive()
	var created []types.ArchiveExecution
	for i := 1; i <= 5; i++ {
		e := types.ArchiveExecution{
			PolicyKey:   []string{"a", "b"}[i%2],
			Kind:        []types.ExecutionKind{types.ExecutionArchive, types.ExecutionRestore}[i/4],
			State:       types.ExecutionInProgress,
			Started:     at(i),
			Total:       int64(10 * i),
			Completed:   int64(i),
			TotalBytes:  int64(1000 * i),
			TriggeredBy: "scheduler",
			Destination: []byte(fmt.Sprintf(`{"tier":"cold-%d"}`, i)),
		}
		got := get[types.ArchiveExecution](t)(as.CreateExecution(ctx, e))
		if got.ID <= 0 || (len(created) > 0 && got.ID <= created[len(created)-1].ID) {
			t.Fatalf("execution ID %d not increasing", got.ID)
		}
		e.ID = got.ID
		same(t, "CreateExecution", e, got)
		created = append(created, e)
	}
	same(
		t,
		"Execution",
		created[2],
		get[types.ArchiveExecution](t)(as.Execution(ctx, created[2].ID)),
	)
	_, err := as.Execution(ctx, created[4].ID+100)
	wantErr(t, err, store.ErrNotFound)

	done := created[2]
	done.State, done.Finished, done.Completed, done.Failed = types.ExecutionCompleteWithErrors, at(
		20,
	), 28, 2
	done.SuccessBytes, done.FailureBytes, done.Destination = 2900, 100, nil
	must(t, as.UpdateExecution(ctx, done))
	same(
		t,
		"Execution after update",
		done,
		get[types.ArchiveExecution](t)(as.Execution(ctx, done.ID)),
	)
	missing := done
	missing.ID = created[4].ID + 100
	wantErr(t, as.UpdateExecution(ctx, missing), store.ErrNotFound)

	ids := func(f store.ExecutionFilter) []int64 {
		var out []int64
		for _, v := range pages(t, 2, func(p store.Page) ([]types.ArchiveExecution, string, error) {
			return as.Executions(ctx, f, p)
		}) {
			out = append(out, v.ID)
		}
		return out
	}
	id := func(i int) int64 { return created[i-1].ID }
	same(t, "Executions", []int64{id(5), id(4), id(3), id(2), id(1)}, ids(store.ExecutionFilter{}))
	same(
		t,
		"Executions(policy b)",
		[]int64{id(5), id(3), id(1)},
		ids(store.ExecutionFilter{PolicyKey: "b"}),
	)
	same(
		t,
		"Executions(restore)",
		[]int64{id(5), id(4)},
		ids(store.ExecutionFilter{Kind: types.ExecutionRestore}),
	)
	_, _, err = as.Executions(ctx, store.ExecutionFilter{}, store.Page{After: store.Cursor("x")})
	wantErr(t, err, store.ErrInvalidCursor)
	_, _, err = as.Executions(ctx, store.ExecutionFilter{}, store.Page{After: "%%%"})
	wantErr(t, err, store.ErrInvalidCursor)
}
