package sql_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platformsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
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

func TestAccReplicationExecutionOptionsCorruptionCannotBecomePrivateRecovery(t *testing.T) {
	for _, corruption := range []string{
		"malformed JSON", "null JSON", "independent column", "target URL column",
		"invalid timeout", "repository binding", "target revision binding", "unreadable independent column", "execution binding",
	} {
		t.Run(corruption, func(t *testing.T) {
			ctx := t.Context()
			store := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			progress := store.Executions()
			binding := executionSQLBinding()
			binding.OneShot, binding.Occurrence, binding.Direction = true, "", model.ReplicationPush
			run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "private-options", binding, time.Unix(1, 0)))
			marker := "private-lab-options-must-not-escape"
			options := model.ExecutionOptions{
				Independent: true, RepositoryKey: binding.Repository.Key, TargetKey: binding.TargetKey, TargetRevision: binding.TargetRevision,
				URL: "https://peer.invalid/artifactory/explicit", Password: marker, SocketTimeout: time.Second,
			}
			storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, options) }))
			query, value := "UPDATE replication_execution_options SET options_json=? WHERE run_id=?", any("")
			corrupt := options
			switch corruption {
			case "malformed JSON":
				value = "{" + marker
			case "null JSON":
				value = "null" // SQL NULL is excluded by the existing NOT NULL constraint.
			case "independent column":
				query, value = "UPDATE replication_execution_options SET independent=? WHERE run_id=?", false
			case "target URL column":
				query, value = "UPDATE replication_execution_options SET target_url=? WHERE run_id=?", marker
			case "unreadable independent column":
				query, value = "UPDATE replication_execution_options SET independent=? WHERE run_id=?", "invalid-boolean"
			case "execution binding":
				query, value = "UPDATE replication_executions SET binding_json=? WHERE run_id=?", "{"+marker
			default:
				switch corruption {
				case "invalid timeout":
					corrupt.SocketTimeout = -time.Second
				case "repository binding":
					corrupt.RepositoryKey = marker
				case "target revision binding":
					corrupt.TargetRevision = marker
				}
				document, err := json.Marshal(corrupt)
				storekit.Must(t, err)
				value = string(document)
			}
			_, err := store.DB().ExecContext(ctx, query, value, run.Reference.ID)
			storekit.Must(t, err)
			got, err := progress.Options(ctx, run.Reference)
			if !errors.Is(err, kernel.ErrStore) || strings.Contains(err.Error(), marker) || got != (model.ExecutionOptions{}) {
				t.Fatal("corrupt execution options became usable or disclosed private values", err)
			}
			changed := options
			changed.Password = "changed-" + marker
			err = store.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, changed) })
			if !errors.Is(err, kernel.ErrStore) || strings.Contains(err.Error(), marker) {
				t.Fatal("changed replay replaced corrupt private options or disclosed their values", err)
			}
			if corruption == "execution binding" {
				document, err := json.Marshal(binding)
				storekit.Must(t, err)
				_, err = store.DB().ExecContext(ctx, "UPDATE replication_executions SET binding_json=? WHERE run_id=?", string(document), run.Reference.ID)
				storekit.Must(t, err)
			} else {
				document, err := json.Marshal(options)
				storekit.Must(t, err)
				_, err = store.DB().ExecContext(ctx,
					"UPDATE replication_execution_options SET options_json=?,independent=?,target_url=? WHERE run_id=?",
					string(document), options.Independent, options.URL, run.Reference.ID)
				storekit.Must(t, err)
			}
			storekit.Must(t, store.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, options) }))
			storekit.Same(t, "repaired storage recovers exact private options", options,
				storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
			storekit.Same(t, "corrupt options preserve execution provenance", run,
				storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
		})
	}
}

func TestAccReplicationExecutionOptionsInsertFaultRollsBackCompoundAdmission(t *testing.T) {
	ctx := t.Context()
	db := sqltest.OpenSQLite(t, platformsql.Module, fedsql.Module)
	store, platform := fedsql.New(db), platformsql.New(db)
	progress, queue := store.Executions(), platform.WorkQueue()
	prior := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "prior-execution", executionSQLBinding(), time.Unix(1, 0)))
	next := prior
	next.Version++
	next.Phase, next.Updated = model.ExecutionRunning, time.Unix(2, 0)
	binding := executionSQLBinding()
	binding.OneShot, binding.Occurrence = true, ""
	options := model.ExecutionOptions{URL: "https://peer.invalid/artifactory/target", Password: "private-lab-admission-password"}
	sqltest.Exec(t, store.DB(), sqltest.FailTrigger("INSERT", "replication_execution_options"))
	var admitted model.ExecutionRun
	admit := func(ctx context.Context) error {
		if err := progress.Advance(ctx, prior.Version, next); err != nil {
			return err
		}
		var err error
		admitted, err = progress.Create(ctx, "failed-admission", binding, time.Unix(2, 0))
		if err != nil {
			return err
		}
		// Write the queue reference before the fault to prove it shares the rollback.
		if err := queue.Enqueue(ctx, "replication:run", admitted.Reference.ID, nil, time.Unix(2, 0)); err != nil {
			return err
		}
		return progress.PutOptions(ctx, admitted.Reference, options)
	}
	err := store.Tx(ctx, admit)
	if !errors.Is(err, kernel.ErrStore) || errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) || strings.Contains(err.Error(), options.Password) {
		t.Fatal("options insert fault was hidden, misclassified or disclosed the password", err)
	}
	storekit.Same(t, "failed admission preserves prior run", prior, storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, prior.Reference)))
	_, err = progress.Run(ctx, admitted.Reference)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	got, err := progress.Options(ctx, admitted.Reference)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Same(t, "failed admission leaves no private options", model.ExecutionOptions{}, got)
	_, err = queue.Get(ctx, "replication:run", admitted.Reference.ID)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	sqltest.Exec(t, store.DB(), "DROP TRIGGER ops_fail_insert_replication_execution_options")
	storekit.Must(t, store.Tx(ctx, admit))
	storekit.Same(t, "admission without the fault commits prior progress", next,
		storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, prior.Reference)))
	storekit.Same(t, "admission without the fault commits its run", admitted,
		storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, admitted.Reference)))
	storekit.Same(t, "admission without the fault commits its private options", options,
		storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, admitted.Reference)))
	_, err = queue.Get(ctx, "replication:run", admitted.Reference.ID)
	storekit.Must(t, err)
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
