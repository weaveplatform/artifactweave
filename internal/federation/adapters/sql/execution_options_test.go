package sql_test

import (
	"context"
	"strings"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccReplicationExecutionOptionsSealExactLifetime(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{fedsql.Module}, fedsql.New, func(t *testing.T, newStore func(*testing.T) *fedsql.Store) {
		store := newStore(t)
		binding := executionSQLBinding()
		binding.OneShot, binding.Occurrence = true, ""
		options := model.ExecutionOptions{URL: "https://peer.invalid/artifactory/target", Password: "private-lab-run-password", SyncProperties: true}
		var refs []model.ExecutionReference
		for _, id := range []string{"run:one/", "run:two/"} {
			run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(t.Context(), id, binding, time.Unix(1, 0)))
			refs = append(refs, run.Reference)
			storekit.Must(t, store.Tx(t.Context(), func(ctx context.Context) error { return store.Executions().PutOptions(ctx, run.Reference, options) }))
			storekit.Same(t, "same resolved secret replays despite randomized sealing", options,
				storekit.Get[model.ExecutionOptions](t)(store.Executions().Options(t.Context(), run.Reference)))
		}
		var ciphertext []byte
		var document string
		query := store.Dialect().Rebind("SELECT password,options_json FROM replication_execution_options WHERE run_id=?")
		storekit.Must(t, store.DB().QueryRowContext(t.Context(), query, refs[0].ID).Scan(&ciphertext, &document))
		if !crypt.IsSealed(ciphertext) || strings.Contains(document, options.Password) {
			t.Fatal("execution password was stored outside its sealed column")
		}
		reader := fedsql.New(dialect.New(store.DB(), store.Dialect(), nil))
		_, err := reader.Executions().Options(t.Context(), refs[0])
		storekit.WantErr(t, err, dialect.ErrNoKeyring)
		_, err = reader.Executions().Run(t.Context(), refs[0])
		storekit.Must(t, err) // Secret-free execution provenance needs no keyring.
		query = store.Dialect().Rebind("UPDATE replication_execution_options SET password=? WHERE run_id=?")
		_, err = store.DB().ExecContext(t.Context(), query, ciphertext, refs[1].ID)
		storekit.Must(t, err)
		if _, err := store.Executions().Options(t.Context(), refs[1]); err == nil || strings.Contains(err.Error(), options.Password) {
			t.Fatal("a transplanted execution password was accepted or disclosed")
		}
	}, sqltest.WithPostgres())
}

func TestAccReplicationExplicitStatusNeedsNeitherKeyringNorPrivateBinding(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{fedsql.Module}, fedsql.New, func(t *testing.T, newStore func(*testing.T) *fedsql.Store) {
		store := newStore(t)
		binding := executionSQLBinding()
		binding.OneShot, binding.Occurrence, binding.Direction = true, "", model.ReplicationPush
		run := storekit.Get[model.ExecutionRun](t)(store.Executions().Create(t.Context(), "explicit-run", binding, time.Unix(1, 0)))
		options := model.ExecutionOptions{
			Independent: true, RepositoryKey: binding.Repository.Key, TargetKey: binding.TargetKey, TargetRevision: binding.TargetRevision,
			URL: "https://peer.invalid/artifactory/explicit", Password: "private-lab-explicit-password", SocketTimeout: time.Second,
		} // #nosec G101 -- fixture-only credential
		storekit.Must(t, store.Tx(t.Context(), func(ctx context.Context) error { return store.Executions().PutOptions(ctx, run.Reference, options) }))
		reader := fedsql.New(dialect.New(store.DB(), store.Dialect(), nil))
		_, err := reader.Executions().Options(t.Context(), run.Reference)
		storekit.WantErr(t, err, dialect.ErrNoKeyring)
		query := store.Dialect().Rebind("UPDATE replication_executions SET binding_json=? WHERE run_id=?")
		_, err = store.DB().ExecContext(t.Context(), query, "invalid-private-binding-must-not-be-opened", run.Reference.ID)
		storekit.Must(t, err)
		targets, next, err := reader.Executions().ExplicitTargets(t.Context(), binding.Repository.Key, binding.Repository.Incarnation, kernel.Page{Limit: 1})
		storekit.Must(t, err)
		if len(targets) != 1 || targets[0].URL != options.URL || next != "" {
			t.Fatal("secret-free explicit target projection failed")
		}
		state := storekit.Get[model.ExecutionPathState](t)(reader.Executions().PathStatus(t.Context(), binding.TargetKey, binding.Repository.Incarnation, ""))
		storekit.Same(t, "secret-free independent execution status", model.ReplicationIncomplete, state.Latest.Status)
	}, sqltest.WithPostgres())
}
