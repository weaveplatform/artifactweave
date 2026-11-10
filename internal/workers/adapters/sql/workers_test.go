package sql_test

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	wrksql "github.com/weaveplatform/artifactweave/internal/workers/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/workers/core/model"
	"github.com/weaveplatform/artifactweave/internal/workers/core/port"
)

// The contract suite covers the worker store's behaviour; these drive its
// driver-failure paths, which a healthy database never takes: a closed
// pool, rows corrupted behind the store's back, and statements a trigger
// makes fail halfway through a transaction. They use SQLite because its
// dynamic typing lets a test store a value the column type forbids.

// workerStore opens a SQLite store, sealed with the test keyring, that
// holds the workers module's tables alone and the worker w with the secret
// token and the execution e1.
func workerStore(t *testing.T) *wrksql.Store {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	s := wrksql.New(sqltest.OpenSQLite(t, wrksql.Module))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Workers().Create(ctx, model.Worker{Key: "w", Action: "AFTER_CREATE"}))
	must(s.Workers().PutSecret(ctx, "w", "token", "s3cret"))
	must(
		s.Workers().
			AddExecution(ctx, model.WorkerExecution{ID: "e1", WorkerKey: "w", Started: now}),
	)
	return s
}

func TestAccWorkersClosedDatabase(t *testing.T) {
	s := wrksql.New(sqltest.OpenSQLite(t, wrksql.Module))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w := s.Workers()
	sqltest.Fails(t, map[string]error{
		"Workers.Create":       w.Create(ctx, model.Worker{Key: "w"}),
		"Workers.Get":          sqltest.Err(w.Get(ctx, "w")),
		"Workers.Update":       w.Update(ctx, model.Worker{Key: "w"}),
		"Workers.Delete":       w.Delete(ctx, "w"),
		"Workers.List":         sqltest.Err(w.List(ctx, port.WorkerFilter{})),
		"Workers.State":        sqltest.Err(w.State(ctx, "w")),
		"Workers.SetState":     w.SetState(ctx, "w", "k", "v"),
		"Workers.DeleteState":  w.DeleteState(ctx, "w", "k"),
		"Workers.PutSecret":    w.PutSecret(ctx, "w", "k", "v"),
		"Workers.Secrets":      sqltest.Err(w.Secrets(ctx, "w")),
		"Workers.DeleteSecret": w.DeleteSecret(ctx, "w", "k"),
		"Workers.AddExecution": w.AddExecution(
			ctx,
			model.WorkerExecution{ID: "e", WorkerKey: "w"},
		),
		"Workers.Execution":       sqltest.Err(w.Execution(ctx, "e")),
		"Workers.Executions":      sqltest.Err3(w.Executions(ctx, "", kernel.Page{})),
		"Workers.PruneExecutions": sqltest.Err(w.PruneExecutions(ctx, time.Now())),
	})
}

func TestAccWorkersCorruptRows(t *testing.T) {
	s := workerStore(t)
	ctx := context.Background()
	w := s.Workers()
	if err := w.PutSecret(ctx, "w", "other", "x"); err != nil {
		t.Fatal(err)
	}
	// Text where a number or JSON document belongs.
	sqltest.Exec(t, s.DB(),
		"UPDATE workers SET filter_doc = '{'",
		"UPDATE worker_executions SET duration_ms = 'x'",
		// A value sealed for another row fails to open.
		"UPDATE worker_secrets SET value = (SELECT value FROM worker_secrets WHERE name = 'other')"+
			" WHERE name = 'token'",
	)
	sqltest.Fails(t, map[string]error{
		"Workers.Get":        sqltest.Err(w.Get(ctx, "w")),
		"Workers.List":       sqltest.Err(w.List(ctx, port.WorkerFilter{})),
		"Workers.Secrets":    sqltest.Err(w.Secrets(ctx, "w")),
		"Workers.Execution":  sqltest.Err(w.Execution(ctx, "e1")),
		"Workers.Executions": sqltest.Err3(w.Executions(ctx, "", kernel.Page{})),
	})
}

func TestAccWorkersFailingStatements(t *testing.T) {
	s := workerStore(t)
	ctx := context.Background()
	w := s.Workers()
	for _, table := range []string{"worker_state", "worker_secrets", "worker_executions"} {
		sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", table))
	}
	sqltest.Fails(t, map[string]error{
		"Workers.SetState":  w.SetState(ctx, "w", "k", "v"),
		"Workers.PutSecret": w.PutSecret(ctx, "w", "k", "v"),
		"Workers.AddExecution": w.AddExecution(
			ctx,
			model.WorkerExecution{ID: "e2", WorkerKey: "w"},
		),
	})
}
