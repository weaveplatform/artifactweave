package sql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccReplicationCounterMismatchCannotEraseOrSettleRetainedIntent(t *testing.T) {
	ctx := t.Context()
	store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
	run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(ctx, "inconsistent-counts", executionSQLBinding(), time.Unix(1, 0)))
	proof := sqlEffect(run)
	storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, proof) }))
	// Zero satisfies the schema's nonnegative CHECK while disagreeing with the
	// still-retained pending intent. Recovery must classify corrupted store state.
	_, err := store.DB().ExecContext(ctx, "UPDATE replication_executions SET pending=0 WHERE run_id=?", run.Reference.ID)
	storekit.Must(t, err)
	retained := storekit.Get[model.ExecutionRun](t)(store.Executions().Run(ctx, run.Reference))
	next := proof
	next.Version++
	next.Outcome, next.Paths, next.Updated = model.EffectSucceeded, []string{proof.Path}, time.Unix(3, 0)
	err = store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, next) })
	if !errors.Is(err, kernel.ErrStore) || errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("inconsistent stored counters became settled success or blamed valid new evidence", err)
	}
	storekit.Same(t, "counter inconsistency preserves exact execution", retained, storekit.Get[model.ExecutionRun](t)(store.Executions().Run(ctx, run.Reference)))
	storekit.Same(
		t,
		"counter inconsistency preserves exact pending proof",
		proof,
		storekit.Get[model.ExecutionEffect](t)(store.Executions().Effect(ctx, run.Reference, proof.OperationKey)),
	)
	_, err = store.DB().ExecContext(ctx, "UPDATE replication_executions SET pending=1 WHERE run_id=?", run.Reference.ID)
	storekit.Must(t, err)
	storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, next) }))
	storekit.Same(
		t,
		"repaired counts permit exactly one real settlement",
		model.ExecutionOutcomes{Succeeded: 1},
		storekit.Get[model.ExecutionRun](t)(store.Executions().Run(ctx, run.Reference)).Outcomes,
	)
}

func TestAccReplicationPathIndexReplacementFailurePreservesPendingProofAndCounts(t *testing.T) {
	ctx := t.Context()
	store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
	run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(ctx, "path-replacement-fault", executionSQLBinding(), time.Unix(1, 0)))
	proof := sqlEffect(run)
	storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, proof) }))
	retained := storekit.Get[model.ExecutionRun](t)(store.Executions().Run(ctx, run.Reference))
	next := proof
	next.Version++
	next.Outcome, next.Paths, next.Updated = model.EffectSucceeded, []string{proof.Path, proof.Path + ".sha256"}, time.Unix(3, 0)
	sqltest.Exec(t, store.DB(), sqltest.FailTrigger("DELETE", "replication_execution_effect_paths"))
	err := store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, next) })
	if !errors.Is(err, kernel.ErrStore) || errors.Is(err, kernel.ErrConflict) {
		t.Fatal("path index replacement failure became committed effect or identity conflict", err)
	}
	storekit.Same(t, "failed path replacement rolls back exact execution", retained, storekit.Get[model.ExecutionRun](t)(store.Executions().Run(ctx, run.Reference)))
	storekit.Same(
		t,
		"failed path replacement rolls back pending intent",
		proof,
		storekit.Get[model.ExecutionEffect](t)(store.Executions().Effect(ctx, run.Reference, proof.OperationKey)),
	)
	var count int
	storekit.Must(t, store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM replication_execution_effect_paths WHERE run_id=?", run.Reference.ID).Scan(&count))
	storekit.Same(t, "failed path replacement preserves original selected index", 1, count)
	sqltest.Exec(t, store.DB(), "DROP TRIGGER ops_fail_delete_replication_execution_effect_paths")
	storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, next) }))
	storekit.Same(
		t,
		"healthy path replacement commits exact companion proof",
		next,
		storekit.Get[model.ExecutionEffect](t)(store.Executions().Effect(ctx, run.Reference, proof.OperationKey)),
	)
	storekit.Must(t, store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM replication_execution_effect_paths WHERE run_id=?", run.Reference.ID).Scan(&count))
	storekit.Same(t, "healthy path replacement indexes selected and companion paths", 2, count)
}
