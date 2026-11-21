package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func executionSQLBinding() model.ExecutionBinding {
	return model.ExecutionBinding{
		TargetKey: "target", TargetRevision: "current-config",
		Repository: artifactsapi.TransferRepository{Key: "remote", Incarnation: "owning-remote", Generation: "source-generation", PackageType: repo.Generic},
		Direction:  model.ReplicationPull, Root: "selected", Credential: kernel.CredentialReference{Name: "author", TokenID: "original-token-reference"},
		Occurrence: "stable-quartz-fire", TransferID: "pull-run", TransferIncarnation: "pull-lifetime",
	}
}

func TestAccReplicationExecutionReopenRetainsOccurrenceAndActualOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replication-execution.db")
	open := func() *fedsql.Store {
		connection, err := sqlite.OpenDB(path, sqlite.Options{})
		storekit.Must(t, err)
		t.Cleanup(func() { storekit.Must(t, connection.Close()) })
		db := dialect.New(connection, sqlite.Dialect, nil)
		_, err = dialect.Migrate(t.Context(), db.DB(), db.Dialect(), []dialect.Module{fedsql.Module}, "")
		storekit.Must(t, err)
		storekit.Must(t, fedsql.Module.AfterMigrate(t.Context(), db))
		return fedsql.New(db)
	}
	first := open()
	run := storekit.Get[model.ExecutionRun](t)(first.Executions().Create(t.Context(), "execution", executionSQLBinding(), time.Unix(1, 0)))
	next := run
	next.Version++
	next.Phase, next.Updated = model.ExecutionAborted, time.Unix(2, 0)
	next.Completed, next.Failure = next.Updated, model.ExecutionRetryExhausted
	next.Outcomes = model.ExecutionOutcomes{Succeeded: 2, Pending: 1}
	storekit.Must(t, first.Tx(t.Context(), func(ctx context.Context) error { return first.Executions().Advance(ctx, run.Version, next) }))
	storekit.Must(t, first.DB().Close())
	second := open()
	storekit.Same(t, "reopened dispatch", next, storekit.Get[model.ExecutionRun](t)(second.Executions().Run(t.Context(), next.Reference)))
	storekit.Same(t, "reopened occurrence", next, storekit.Get[model.ExecutionRun](t)(second.Executions().ForOccurrence(t.Context(), next.Binding.Occurrence)))
	storekit.Same(
		t,
		"retry retains outcomes",
		next,
		storekit.Get[model.ExecutionRun](t)(second.Executions().Create(t.Context(), "execution", executionSQLBinding(), time.Unix(9, 0))),
	)
	storekit.Same(t, "restart retains truthful failure", model.ReplicationPartialFailure, next.Status())
}

func TestAccReplicationExecutionRejectsCorruptPrivateProvenance(t *testing.T) {
	for _, field := range []string{"binding_json", "target_key", "repo_key", "repo_incarnation", "direction", "occurrence", "failure_reason"} {
		t.Run(field, func(t *testing.T) {
			store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(t.Context(), "execution", executionSQLBinding(), time.Unix(1, 0)))
			marker := "private-must-not-escape"
			// Column names are a closed test list, never request input.
			_, err := store.DB().ExecContext(t.Context(), "UPDATE replication_executions SET "+field+"=? WHERE run_id=?", marker, run.Reference.ID)
			storekit.Must(t, err)
			_, err = store.Executions().Run(t.Context(), run.Reference)
			if !errors.Is(err, kernel.ErrStore) || strings.Contains(err.Error(), marker) {
				t.Fatal("invalid persisted provenance was accepted or disclosed", err)
			}
		})
	}
}

func TestAccReplicationExecutionWriteFaultLeavesPriorProgress(t *testing.T) {
	for _, operation := range []string{"INSERT", "UPDATE"} {
		t.Run(operation, func(t *testing.T) {
			store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(t.Context(), "execution", executionSQLBinding(), time.Unix(1, 0)))
			sqltest.Exec(t, store.DB(), sqltest.FailTrigger(operation, "replication_executions"))
			err := store.Tx(t.Context(), func(ctx context.Context) error {
				if operation == "INSERT" {
					binding := executionSQLBinding()
					binding.Occurrence = "another-fire"
					_, err := store.Executions().Create(ctx, "another-execution", binding, time.Unix(2, 0))
					return err
				}
				next := run
				next.Version++
				next.Phase, next.Updated = model.ExecutionRunning, time.Unix(2, 0)
				return store.Executions().Advance(ctx, run.Version, next)
			})
			if err == nil || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
				t.Fatal("database failure became success or an ordinary identity conflict", err)
			}
			storekit.Same(t, "write fault preserves prior execution", run,
				storekit.Get[model.ExecutionRun](t)(store.Executions().Run(t.Context(), run.Reference)))
		})
	}
}
