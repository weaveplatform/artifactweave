package sql_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccReplicationClosedDatabaseCannotBecomeMissingPrivateExecution(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "private-execution.db")
	keys, err := crypt.New(bytes.Repeat([]byte{1}, 32))
	storekit.Must(t, err)
	open := func() *fedsql.Store {
		connection, err := sqlite.OpenDB(path, sqlite.Options{})
		storekit.Must(t, err)
		t.Cleanup(func() { storekit.Must(t, connection.Close()) })
		db := dialect.New(connection, sqlite.Dialect, keys)
		_, err = dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{fedsql.Module}, "")
		storekit.Must(t, err)
		storekit.Must(t, fedsql.Module.AfterMigrate(ctx, db))
		return fedsql.New(db)
	}
	first := open()
	binding := executionSQLBinding()
	binding.OneShot, binding.Occurrence = true, ""
	binding.Direction = model.ReplicationPush
	run := storekit.Get[model.ExecutionRun](t)(first.Executions().Create(ctx, "retained-during-outage", binding, time.Unix(1, 0)))
	options := model.ExecutionOptions{
		Independent:    true,
		RepositoryKey:  binding.Repository.Key,
		TargetKey:      binding.TargetKey,
		TargetRevision: binding.TargetRevision,
		URL:            "https://peer.invalid/artifactory/target",
		Password:       "private-file-recovery-control",
		SocketTimeout:  time.Second,
	}
	proof := sqlEffect(run)
	storekit.Must(t, first.Tx(ctx, func(ctx context.Context) error {
		if err := first.Executions().PutOptions(ctx, run.Reference, options); err != nil {
			return err
		}
		return first.Executions().RecordEffect(ctx, proof)
	}))
	retained := storekit.Get[model.ExecutionRun](t)(first.Executions().Run(ctx, run.Reference))
	storekit.Must(t, first.DB().Close())
	got, err := first.Executions().Run(ctx, run.Reference)
	if !errors.Is(err, kernel.ErrStore) || errors.Is(err, kernel.ErrNotFound) || got != (model.ExecutionRun{}) {
		t.Fatal("closed database turned retained dispatch into missing or usable state", err)
	}
	private, err := first.Executions().Options(ctx, run.Reference)
	if !errors.Is(err, kernel.ErrStore) || errors.Is(err, kernel.ErrNotFound) || private != (model.ExecutionOptions{}) {
		t.Fatal("closed database restored private options or treated them as absent", err)
	}
	count, present, err := first.Executions().SelectedPaths(ctx, run.Reference, proof.Path)
	if !errors.Is(err, kernel.ErrStore) || count != 0 || present {
		t.Fatal("closed database authorized a fresh bounded selection", err)
	}
	destinations, next, err := first.Executions().ExplicitTargets(ctx, binding.Repository.Key, binding.Repository.Incarnation, kernel.Page{})
	if !errors.Is(err, kernel.ErrStore) || len(destinations) != 0 || next != "" {
		t.Fatal("closed database disclosed a partial explicit-target view", err)
	}
	second := open()
	storekit.Same(t, "reopen retains exact dispatch and counts", retained, storekit.Get[model.ExecutionRun](t)(second.Executions().Run(ctx, run.Reference)))
	storekit.Same(t, "reopen retains original sealed options", options, storekit.Get[model.ExecutionOptions](t)(second.Executions().Options(ctx, run.Reference)))
	storekit.Same(t, "reopen retains exact pending intent", proof, storekit.Get[model.ExecutionEffect](t)(second.Executions().Effect(ctx, run.Reference, proof.OperationKey)))
	destinations, next, err = second.Executions().ExplicitTargets(ctx, binding.Repository.Key, binding.Repository.Incarnation, kernel.Page{})
	storekit.Must(t, err)
	if len(destinations) != 1 || destinations[0].Key != binding.TargetKey || destinations[0].URL != options.URL || next != "" {
		t.Fatal("reopened database lost the actual explicit destination")
	}
	completed := proof
	completed.Version++
	completed.Outcome, completed.Paths, completed.Updated = model.EffectSucceeded, []string{proof.Path}, time.Unix(3, 0)
	storekit.Must(t, second.Tx(ctx, func(ctx context.Context) error { return second.Executions().RecordEffect(ctx, completed) }))
	storekit.Same(
		t,
		"recovered selected intent counts once",
		model.ExecutionOutcomes{Succeeded: 1},
		storekit.Get[model.ExecutionRun](t)(second.Executions().Run(ctx, run.Reference)).Outcomes,
	)
}
