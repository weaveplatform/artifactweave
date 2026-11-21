package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

var errExecutionRollback = errors.New("rollback execution test")

func executionBinding(occurrence string) model.ExecutionBinding {
	return model.ExecutionBinding{
		TargetKey: "target", TargetRevision: "configuration",
		Repository: artifactsapi.TransferRepository{Key: "source", Incarnation: "repository-lifetime", Generation: "generation", PackageType: repo.Generic},
		Direction:  model.ReplicationPush, Credential: kernel.CredentialReference{Name: "author", TokenID: "token-reference"},
		Occurrence: occurrence, TransferID: "transfer", TransferIncarnation: "transfer-lifetime",
	}
}

func runExecutions(t *testing.T, newStore Factory) {
	t.Run("Executions", func(t *testing.T) {
		t.Run("immutable identity and occurrence", func(t *testing.T) { executionIdentity(t, newStore(t)) })
		t.Run("transactional recovery", func(t *testing.T) { executionRecovery(t, newStore(t)) })
		t.Run("closed failure recovery", func(t *testing.T) { executionFailureRecovery(t, newStore(t)) })
		t.Run("selected effects and replay", func(t *testing.T) { executionEffects(t, newStore(t)) })
		t.Run("scoped secret-free status", func(t *testing.T) { executionScopedStatus(t, newStore(t)) })
	})
}

func executionIdentity(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "run", executionBinding("logical-fire"), storekit.At(1)))
	storekit.Same(t, "retry retains lifetime", run, storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "run", executionBinding("logical-fire"), storekit.At(9))))
	storekit.Same(t, "occurrence retains run", run, storekit.Get[model.ExecutionRun](t)(progress.ForOccurrence(ctx, "logical-fire")))
	for _, change := range []func(*model.ExecutionBinding){
		func(binding *model.ExecutionBinding) { binding.TargetRevision = "changed" },
		func(binding *model.ExecutionBinding) { binding.Repository.Incarnation = "recreated" },
		func(binding *model.ExecutionBinding) { binding.TransferIncarnation = "another-transfer" },
		func(binding *model.ExecutionBinding) { binding.Credential.TokenID = "another-token" },
	} {
		binding := executionBinding("logical-fire")
		change(&binding)
		_, err := progress.Create(ctx, "run", binding, storekit.At(9))
		storekit.WantErr(t, err, kernel.ErrConflict)
	}
	_, err := progress.Create(ctx, "another-run", executionBinding("logical-fire"), storekit.At(9))
	storekit.WantErr(t, err, kernel.ErrConflict)
	_, err = progress.ForOccurrence(ctx, "")
	storekit.WantErr(t, err, kernel.ErrInvalid)
	wrong := run.Reference
	wrong.Incarnation = "wrong-lifetime"
	_, err = progress.Run(ctx, wrong)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	// Unscheduled runs have distinct NULL/empty occurrence identities.
	_, err = progress.Create(ctx, "manual-one", executionBinding(""), storekit.At(2))
	storekit.Must(t, err)
	_, err = progress.Create(ctx, "manual-two", executionBinding(""), storekit.At(2))
	storekit.Must(t, err)
}

func executionRecovery(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "run", executionBinding(""), storekit.At(1)))
	next := run
	next.Version++
	next.Phase, next.Updated = model.ExecutionRunning, storekit.At(2)
	next.Outcomes = model.ExecutionOutcomes{Succeeded: 1, Pending: 1}
	storekit.WantErr(t, progress.Advance(ctx, run.Version, next), kernel.ErrInvalid)
	abort := errExecutionRollback
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := progress.Advance(ctx, run.Version, next); err != nil {
			return fmt.Errorf("advance rollback fixture: %w", err)
		}
		if _, err := progress.Create(ctx, "rolled-back", executionBinding("rolled-fire"), storekit.At(2)); err != nil {
			return fmt.Errorf("create rollback fixture: %w", err)
		}
		return abort
	})
	storekit.WantErr(t, err, abort)
	storekit.Same(t, "rollback preserves prior outcomes", run, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	_, err = progress.ForOccurrence(ctx, "rolled-fire")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, run.Version, next) }))
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, run.Version, next) }), kernel.ErrConflict)
	completed := next
	completed.Version++
	completed.Phase, completed.Completed, completed.Updated = model.ExecutionComplete, storekit.At(3), storekit.At(3)
	completed.Outcomes = model.ExecutionOutcomes{Succeeded: 1, Failed: 1}
	storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, next.Version, completed) }))
	retained := storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference))
	storekit.Same(t, "retained actual outcomes", completed, retained)
	storekit.Same(t, "partial failure is not OK", model.ReplicationPartialFailure, retained.Status())
	completed.Version++
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return progress.Advance(ctx, retained.Version, completed) }), kernel.ErrConflict)
}

func executionFailureRecovery(t *testing.T, db port.Store) {
	t.Helper()
	ctx := t.Context()
	run := storekit.Get[model.ExecutionRun](t)(db.Executions().Create(ctx, "failed-run", executionBinding(""), storekit.At(1)))
	next := run
	next.Version++
	next.Phase = model.ExecutionAborted
	next.Failure = model.ExecutionAuthorityDenied
	next.Updated, next.Completed = storekit.At(2), storekit.At(2)
	next.Outcomes = model.ExecutionOutcomes{Succeeded: 1, Pending: 1}
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := db.Executions().Advance(ctx, run.Version, next); err != nil {
			return fmt.Errorf("advance failed execution rollback fixture: %w", err)
		}
		return errExecutionRollback
	})
	storekit.WantErr(t, err, errExecutionRollback)
	storekit.Same(t, "failed publication preserves unfinished dispatch", run, storekit.Get[model.ExecutionRun](t)(db.Executions().Run(ctx, run.Reference)))
	storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return db.Executions().Advance(ctx, run.Version, next) }))
	storekit.Same(t, "closed failure and real counts survive", next, storekit.Get[model.ExecutionRun](t)(db.Executions().Run(ctx, run.Reference)))
	storekit.Same(t, "admission replay retains failure", next, storekit.Get[model.ExecutionRun](t)(db.Executions().Create(ctx, "failed-run", executionBinding(""), storekit.At(3))))
	storekit.WantErr(t, db.Tx(ctx, func(ctx context.Context) error { return db.Executions().Advance(ctx, run.Version, next) }), kernel.ErrConflict)
}
