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
