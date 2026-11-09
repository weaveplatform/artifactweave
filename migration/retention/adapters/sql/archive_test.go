package sql_test

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
	retsql "github.com/weaveplatform/artifactweave/migration/retention/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
	"github.com/weaveplatform/artifactweave/migration/retention/core/port"
)

// The contract suite covers the archive store's behaviour; these drive its
// driver-failure paths, which a healthy database never takes: a closed
// pool and rows corrupted behind the store's back. They use SQLite because
// its dynamic typing lets a test store a value the column type forbids.

func TestAccArchiveClosedDatabase(t *testing.T) {
	s := retsql.New(sqltest.OpenSQLite(t, retsql.Module))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := s.Archive()
	ex := model.ArchiveExecution{ID: 1}
	sqltest.Fails(t, map[string]error{
		"Archive.CreatePolicy":    a.CreatePolicy(ctx, model.ArchivePolicy{Key: "p"}),
		"Archive.Policy":          sqltest.Err(a.Policy(ctx, "p")),
		"Archive.UpdatePolicy":    a.UpdatePolicy(ctx, model.ArchivePolicy{Key: "p"}),
		"Archive.DeletePolicy":    a.DeletePolicy(ctx, "p"),
		"Archive.Policies":        sqltest.Err(a.Policies(ctx, model.ArchiveSmartV2)),
		"Archive.CreateExecution": sqltest.Err(a.CreateExecution(ctx, ex)),
		"Archive.Execution":       sqltest.Err(a.Execution(ctx, 1)),
		"Archive.UpdateExecution": a.UpdateExecution(ctx, ex),
		"Archive.Executions": sqltest.Err3(
			a.Executions(ctx, port.ExecutionFilter{}, kernel.Page{}),
		),
	})
}

func TestAccArchiveCorruptRows(t *testing.T) {
	s := retsql.New(sqltest.OpenSQLite(t, retsql.Module))
	ctx := context.Background()
	a := s.Archive()
	if err := a.CreatePolicy(
		ctx,
		model.ArchivePolicy{Key: "p", Family: model.ArchiveSmartV2},
	); err != nil {
		t.Fatal(err)
	}
	ex, err := a.CreateExecution(ctx, model.ArchiveExecution{Kind: model.ExecutionArchive})
	if err != nil {
		t.Fatal(err)
	}
	// Text where a number or boolean belongs.
	sqltest.Exec(t, s.DB(),
		"UPDATE archive_policies SET enabled = 'x'",
		"UPDATE archive_executions SET total = 'x'",
	)
	sqltest.Fails(t, map[string]error{
		"Archive.Policy":    sqltest.Err(a.Policy(ctx, "p")),
		"Archive.Policies":  sqltest.Err(a.Policies(ctx, "")),
		"Archive.Execution": sqltest.Err(a.Execution(ctx, ex.ID)),
		"Archive.Executions": sqltest.Err3(
			a.Executions(ctx, port.ExecutionFilter{}, kernel.Page{}),
		),
	})
}
