package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	retsql "github.com/weaveplatform/artifactweave/internal/retention/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// TestAccCleanupUnmigrated runs every cleanup method against a database
// without the schema: each must surface the driver error, never a nil error
// or a misleading ErrNotFound.
func TestAccCleanupUnmigrated(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenDB(filepath.Join(t.TempDir(), "raw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cs := retsql.New(dialect.New(db, sqlite.Dialect, nil)).Cleanup()
	ops := map[string]error{
		"create policy": cs.CreatePolicy(ctx, model.CleanupPolicy{Key: "k"}),
		"policy":        sqltest.Err(cs.Policy(ctx, "k")),
		"update policy": cs.UpdatePolicy(ctx, model.CleanupPolicy{Key: "k"}),
		"delete policy": cs.DeletePolicy(ctx, "k"),
		"policies": sqltest.Err(
			cs.Policies(ctx, port.PolicyFilter{ItemType: "package", ProjectKey: "p"}),
		),
		"create run": sqltest.Err(cs.CreateRun(ctx, model.CleanupRun{PolicyKey: "k"})),
		"run":        sqltest.Err(cs.Run(ctx, 1)),
		"update run": cs.UpdateRun(ctx, model.CleanupRun{ID: 1}),
		"last run":   sqltest.Err(cs.LastRun(ctx, "k")),
		"runs": sqltest.Err3(cs.Runs(ctx, port.RunFilter{
			PolicyKey: "k", ItemType: "package",
			Statuses: []api.RunStatus{api.RunCompleted},
		}, kernel.Page{})),
	}
	for name, err := range ops {
		if err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
