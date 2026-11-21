package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

var errExecutionRollback = errors.New("injected execution transaction rollback")

func testCleanupExecutions(t *testing.T, s port.Store) {
	ctx := t.Context()
	cs := s.Cleanup()
	p := cleanupPolicy("execution-policy", model.CleanupPackage, "alpha")
	storekit.Must(t, cs.CreatePolicy(ctx, p))
	newRun := func() model.CleanupRun {
		r, err := cs.CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: model.CleanupPackage, Status: api.RunInProgress})
		storekit.Must(t, err)
		return r
	}
	r := newRun()
	credential := p.ExecutionCredential
	incarnation := p.ProjectIncarnation
	p.ExecutionCredential = kernel.CredentialReference{}
	p.ProjectIncarnation = ""
	e := model.CleanupExecution{RunID: r.ID, Occurrence: "logical-scheduled-fire", Policy: p, Credential: credential, ProjectIncarnation: incarnation, At: storekit.At(4)}
	storekit.Must(t, cs.CreateExecution(ctx, e))
	storekit.Same(t, "execution roundtrip", e, storekit.Get[model.CleanupExecution](t)(cs.Execution(ctx, r.ID)))
	storekit.Same(t, "occurrence resolves original run", e, storekit.Get[model.CleanupExecution](t)(cs.ExecutionForOccurrence(ctx, e.Occurrence)))
	storekit.WantErr(t, cs.CreateExecution(ctx, e), kernel.ErrConflict)
	other := e
	other.RunID = newRun().ID
	storekit.WantErr(t, cs.CreateExecution(ctx, other), kernel.ErrConflict)
	for range 2 {
		manual := e
		manual.RunID, manual.Occurrence = newRun().ID, ""
		storekit.Must(t, cs.CreateExecution(ctx, manual))
	}
	_, err := cs.ExecutionForOccurrence(ctx, "")
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = cs.Execution(ctx, -1)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = cs.ExecutionForOccurrence(ctx, "missing-fire")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	copy, err := cs.Execution(ctx, r.ID)
	storekit.Must(t, err)
	copy.Policy.Criteria.Repos[0] = "mutated-outside-the-store"
	storekit.Same(t, "execution is a detached copy", e, storekit.Get[model.CleanupExecution](t)(cs.Execution(ctx, r.ID)))
	invalid := e
	invalid.RunID = -1
	storekit.WantErr(t, cs.CreateExecution(ctx, invalid), kernel.ErrInvalid)
	storekit.Must(t, cs.DeletePolicy(ctx, p.Key))
	storekit.Same(t, "history survives policy deletion", e, storekit.Get[model.CleanupExecution](t)(cs.Execution(ctx, r.ID)))
	var rolledID int64
	err = s.Tx(ctx, func(ctx context.Context) error {
		rolled := newRunInTransaction(t, ctx, cs, p.Key)
		rolledID = rolled.ID
		pending := e
		pending.RunID, pending.Occurrence = rolled.ID, "rolled-back-fire"
		storekit.Must(t, cs.CreateExecution(ctx, pending))
		return errExecutionRollback
	})
	storekit.WantErr(t, err, errExecutionRollback)
	_, err = cs.Execution(ctx, rolledID)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	_, err = cs.ExecutionForOccurrence(ctx, "rolled-back-fire")
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func newRunInTransaction(t *testing.T, ctx context.Context, cs port.CleanupStore, key string) model.CleanupRun {
	t.Helper()
	r, err := cs.CreateRun(ctx, model.CleanupRun{PolicyKey: key, ItemType: model.CleanupPackage, Status: api.RunInProgress})
	storekit.Must(t, err)
	return r
}
