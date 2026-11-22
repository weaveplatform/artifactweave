package sql_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func sqlEffect(run model.ExecutionRun) model.ExecutionEffect {
	return model.ExecutionEffect{
		Run: run.Reference, TransferID: run.Binding.TransferID, TransferIncarnation: run.Binding.TransferIncarnation,
		OperationKey: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Version: 1,
		Path: "selected/file", Outcome: model.EffectPending, Updated: time.Unix(2, 0),
	}
}

func TestAccReplicationPathStatusNeedsNeitherSecretsNorPrivateBinding(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{fedsql.Module}, fedsql.New, func(t *testing.T, factory func(*testing.T) *fedsql.Store) {
		store := factory(t)
		binding := executionSQLBinding()
		storekit.Must(t, store.Replications().Put(t.Context(), model.Replication{
			Key: binding.TargetKey, RepoKey: binding.Repository.Key, Password: "private-sealed-lab-password",
		}))
		run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(t.Context(), "selected-run", binding, time.Unix(1, 0)))
		storekit.Must(t, store.Tx(t.Context(), func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, sqlEffect(run)) }))
		reader := fedsql.New(dialect.New(store.DB(), store.Dialect(), nil))
		_, err := reader.Replications().Get(t.Context(), binding.TargetKey)
		storekit.WantErr(t, err, dialect.ErrNoKeyring)
		query := store.Dialect().Rebind("UPDATE replication_executions SET binding_json=? WHERE run_id=?")
		_, err = store.DB().ExecContext(t.Context(), query, "private-invalid-binding-must-not-be-read", run.Reference.ID)
		storekit.Must(t, err)
		_, err = reader.Executions().Run(t.Context(), run.Reference)
		storekit.WantErr(t, err, kernel.ErrStore)
		for _, path := range []string{"", "selected", "selected/file"} {
			state := storekit.Get[model.ExecutionPathState](t)(reader.Executions().PathStatus(t.Context(), binding.TargetKey, binding.Repository.Incarnation, path))
			storekit.Same(t, "secret-free selected status", model.ReplicationIncomplete, state.Latest.Status)
		}
	}, sqltest.WithPostgres())
}

func TestAccReplicationEffectWriteFaultRollsBackEvidenceIndexAndCounts(t *testing.T) {
	for _, table := range []string{"replication_execution_effects", "replication_execution_effect_paths", "replication_executions"} {
		t.Run(table, func(t *testing.T) {
			store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(t.Context(), "run", executionSQLBinding(), time.Unix(1, 0)))
			operation := "INSERT"
			if table == "replication_executions" {
				operation = "UPDATE"
			}
			sqltest.Exec(t, store.DB(), sqltest.FailTrigger(operation, table))
			err := store.Tx(t.Context(), func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, sqlEffect(run)) })
			if err == nil || errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("effect write failure was hidden or misclassified", err)
			}
			storekit.Same(t, "all effect stages roll back counters", run,
				storekit.Get[model.ExecutionRun](t)(store.Executions().Run(t.Context(), run.Reference)))
			_, err = store.Executions().Effect(t.Context(), run.Reference, sqlEffect(run).OperationKey)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			var count int
			storekit.Must(t, store.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM replication_execution_effect_paths").Scan(&count))
			storekit.Same(t, "effect path index rolled back", 0, count)
		})
	}
}

func TestAccReplicationCorruptSelectedProofCannotAuthorizeRecovery(t *testing.T) {
	for _, corruption := range []struct{ name, column, value string }{
		{"malformed effect paths", "effect_paths_json", "[private-corrupt-effect-must-not-escape"},
		{"foreign transfer", "transfer_incarnation", "private-corrupt-effect-must-not-escape"},
		{"unfinished reason", "reason", "private-corrupt-effect-must-not-escape"},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			ctx := t.Context()
			store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			progress := store.Executions()
			run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "corrupt-proof", executionSQLBinding(), time.Unix(1, 0)))
			proof := sqlEffect(run)
			storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return progress.RecordEffect(ctx, proof) }))
			retained := storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference))
			var original string
			query := "SELECT " + corruption.column + " FROM replication_execution_effects WHERE run_id=? AND operation_key=?"
			storekit.Must(t, store.DB().QueryRowContext(ctx, query, run.Reference.ID, proof.OperationKey).Scan(&original))
			update := "UPDATE replication_execution_effects SET " + corruption.column + "=? WHERE run_id=? AND operation_key=?"
			_, err := store.DB().ExecContext(ctx, update, corruption.value, run.Reference.ID, proof.OperationKey)
			storekit.Must(t, err)
			got, err := progress.Effect(ctx, run.Reference, proof.OperationKey)
			if !errors.Is(err, kernel.ErrStore) || strings.Contains(err.Error(), corruption.value) || got.Run != (model.ExecutionReference{}) {
				t.Fatal("corrupt retained proof became usable or disclosed private data", err)
			}
			next := proof
			next.Version++
			next.Outcome, next.Paths, next.Updated = model.EffectSucceeded, []string{proof.Path}, time.Unix(3, 0)
			err = store.Tx(ctx, func(ctx context.Context) error { return progress.RecordEffect(ctx, next) })
			if !errors.Is(err, kernel.ErrStore) || strings.Contains(err.Error(), corruption.value) {
				t.Fatal("recovery replaced corrupt retained evidence or disclosed its data", err)
			}
			storekit.Same(t, "corrupt proof preserves exact execution counts", retained, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
			var unchanged string
			storekit.Must(t, store.DB().QueryRowContext(ctx, query, run.Reference.ID, proof.OperationKey).Scan(&unchanged))
			storekit.Same(t, "refused recovery preserves the original diagnostic row", corruption.value, unchanged)
			_, err = store.DB().ExecContext(ctx, update, original, run.Reference.ID, proof.OperationKey)
			storekit.Must(t, err)
			storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return progress.RecordEffect(ctx, next) }))
			storekit.Same(
				t,
				"repaired proof recovers exactly once",
				model.ExecutionOutcomes{Succeeded: 1},
				storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)).Outcomes,
			)
		})
	}
}

func TestAccReplicationSecretFreeStatusCannotCallCorruptOutcomesSuccessful(t *testing.T) {
	// Negative counters are already excluded by the table's CHECK constraints.
	for _, corruption := range []string{"unknown phase", "complete with pending"} {
		t.Run(corruption, func(t *testing.T) {
			ctx := t.Context()
			store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			binding := executionSQLBinding()
			storekit.Must(
				t,
				store.Replications().Put(ctx, model.Replication{Key: binding.TargetKey, RepoKey: binding.Repository.Key, Password: "sealed-status-corruption-control"}),
			)
			run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(ctx, "status-corruption", binding, time.Unix(1, 0)))
			proof := sqlEffect(run)
			storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return store.Executions().RecordEffect(ctx, proof) }))
			reader := fedsql.New(dialect.New(store.DB(), store.Dialect(), nil))
			query := "UPDATE replication_executions SET phase='retired' WHERE run_id=?"
			if corruption == "complete with pending" {
				query = "UPDATE replication_executions SET phase='complete',completed_at=updated_at WHERE run_id=?"
			}
			_, err := store.DB().ExecContext(ctx, query, run.Reference.ID)
			storekit.Must(t, err)
			_, err = reader.Executions().Run(ctx, run.Reference)
			storekit.WantErr(t, err, kernel.ErrStore)
			state := storekit.Get[model.ExecutionPathState](t)(reader.Executions().PathStatus(ctx, binding.TargetKey, binding.Repository.Incarnation, ""))
			storekit.Same(t, "corrupt secret-free outcomes fail closed", model.ReplicationUnknown, state.Latest.Status)
			_, err = store.DB().ExecContext(ctx, "UPDATE replication_executions SET succeeded=0,phase='running',completed_at=NULL WHERE run_id=?", run.Reference.ID)
			storekit.Must(t, err)
			state = storekit.Get[model.ExecutionPathState](t)(reader.Executions().PathStatus(ctx, binding.TargetKey, binding.Repository.Incarnation, "selected"))
			storekit.Same(t, "repaired execution retains selected incomplete truth", model.ReplicationIncomplete, state.Latest.Status)
		})
	}
}
