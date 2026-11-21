package sql_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retsql "github.com/weaveplatform/artifactweave/internal/retention/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestAccCleanupExecutionSurvivesDatabaseReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "execution.db")
	open := func() *retsql.Store {
		db, err := sqlite.OpenDB(path, sqlite.Options{})
		storekit.Must(t, err)
		t.Cleanup(func() { _ = db.Close() })
		return retsql.New(dialect.New(db, sqlite.Dialect, nil))
	}
	first := open()
	_, err := dialect.Migrate(ctx, first.DB(), sqlite.Dialect, []dialect.Module{retsql.Module}, "")
	storekit.Must(t, err)
	p := model.CleanupPolicy{
		Key: "restart-policy", Revision: "original-revision", ItemType: model.CleanupPackage, Enabled: true,
		ExecutionCredential: kernel.CredentialReference{Name: "original-user", TokenID: "original-verified-token"},
		Description:         "admitted description", Criteria: model.CleanupCriteria{Repos: []string{"**"}},
	}
	storekit.Must(t, first.Cleanup().CreatePolicy(ctx, p))
	r, err := first.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, TriggerID: 123, ItemType: model.CleanupPackage, Status: api.RunInProgress})
	storekit.Must(t, err)
	e := model.CleanupExecution{
		RunID:      r.ID,
		Occurrence: "original-fire",
		Policy:     p,
		Credential: p.ExecutionCredential,
		At:         time.Date(2026, 11, 20, 1, 0, 0, 123456789, time.FixedZone("BST", 3600)),
	}
	storekit.Must(t, first.Cleanup().CreateExecution(ctx, e))
	e.Policy.ExecutionCredential = kernel.CredentialReference{}
	p.Description = "edited after admission"
	storekit.Must(t, first.Cleanup().UpdatePolicy(ctx, p))
	storekit.Must(t, first.Close())
	second := open()
	storekit.Same(t, "private execution survives closing/reopening SQLite", e, storekit.Get[model.CleanupExecution](t)(second.Cleanup().Execution(ctx, r.ID)))
	storekit.Same(t, "policy grant survives restart", p, storekit.Get[model.CleanupPolicy](t)(second.Cleanup().Policy(ctx, p.Key)))
	storekit.Same(t, "scheduled fire preserves original integer run", e, storekit.Get[model.CleanupExecution](t)(second.Cleanup().ExecutionForOccurrence(ctx, e.Occurrence)))
	storekit.Must(t, second.Cleanup().DeletePolicy(ctx, p.Key))
	storekit.Same(t, "deleted policy leaves execution history", e, storekit.Get[model.CleanupExecution](t)(second.Cleanup().Execution(ctx, r.ID)))
}
