package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retsql "github.com/weaveplatform/artifactweave/internal/retention/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func TestAccCleanupReportReadsIgnoreLegacyPayloadColumn(t *testing.T) {
	s := retsql.New(sqltest.OpenSQLite(t, retsql.Module))
	ctx, cs := t.Context(), s.Cleanup()
	p := model.CleanupPolicy{Key: "bounded-report", Revision: "revision", ItemType: model.CleanupPackage}
	storekit.Must(t, cs.CreatePolicy(ctx, p))
	r := storekit.Get[model.CleanupRun](t)(cs.CreateRun(ctx, model.CleanupRun{
		PolicyKey: p.Key, ItemType: p.ItemType, Status: api.RunCompleted, DryRun: true,
		TotalItems: 1, Report: []byte(`{"legacy":"private"}`),
	}))
	r.TriggerID = r.ID
	storekit.Must(t, cs.UpdateRun(ctx, r))
	storekit.Must(t, cs.CreateExecution(ctx, model.CleanupExecution{
		RunID: r.ID, Policy: p, Credential: kernel.CredentialReference{Name: "reader", TokenID: "verified"}, At: time.Now(),
	}))
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		if err := cs.CreateProgress(ctx, model.CleanupProgress{RunID: r.ID, Revision: 1, Phase: model.CleanupRepositories}); err != nil {
			return err
		}
		_, err := cs.CreateEntry(ctx, model.CleanupEntry{
			RunID: r.ID, EntityID: 1, VersionID: 1, Revision: 1, Kind: model.CleanupItemEntry, State: model.CleanupSelected,
			Snapshot: artifactsapi.IndexFile{ID: 1, Version: artifactsapi.IndexVersion{ID: 1}, Size: 5},
		})
		return err
	}))
	// A private disposable schema fault proves that every report query avoids
	// the legacy column itself, rather than loading and then discarding it.
	sqltest.Exec(t, s.DB(), "ALTER TABLE cleanup_runs DROP COLUMN report")
	if _, err := cs.Run(ctx, r.ID); err == nil {
		t.Fatal("legacy full-run control unexpectedly ignored its report column")
	}
	metadata := r
	metadata.Report = nil
	storekit.Same(t, "bounded metadata", metadata, storekit.Get[model.CleanupRun](t)(cs.RunMetadata(ctx, r.ID)))
	storekit.Same(t, "bounded totals", model.CleanupOutcomeTotals{
		Items: 1, Selected: 1, SelectedVirtualBytes: 5, SelectedContentBytes: 5,
	}, storekit.Get[model.CleanupOutcomeTotals](t)(cs.OutcomeTotals(ctx, r.ID)))
	page := storekit.Get[port.RunPage](t)(cs.RunPage(ctx, port.RunListing{Scopes: []port.RunScope{{ProjectKey: ""}}, Limit: 1}))
	if page.Total != 1 || len(page.Runs) != 1 || page.Runs[0].Run.ID != r.ID {
		t.Fatal("bounded run page lost retained history")
	}
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error { return cs.MarkReportDownloaded(ctx, r.ID) }))
	metadata.ReportDownloaded = true
	storekit.Same(t, "bounded completion marker", metadata, storekit.Get[model.CleanupRun](t)(cs.RunMetadata(ctx, r.ID)))
}

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
	execution := model.CleanupExecution{
		RunID: 1, Occurrence: "fire", Policy: model.CleanupPolicy{Key: "p", Revision: "rev", ItemType: model.CleanupPackage},
		Credential: kernel.CredentialReference{Name: "caller", TokenID: "verified-token"}, At: time.Now(),
	}
	ops := map[string]error{
		"create policy": cs.CreatePolicy(ctx, model.CleanupPolicy{Key: "k"}),
		"policy":        sqltest.Err(cs.Policy(ctx, "k")),
		"update policy": cs.UpdatePolicy(ctx, model.CleanupPolicy{Key: "k"}),
		"delete policy": cs.DeletePolicy(ctx, "k"),
		"policies": sqltest.Err(
			cs.Policies(ctx, port.PolicyFilter{ItemType: "package", ProjectKey: "p"}),
		),
		"create run":       sqltest.Err(cs.CreateRun(ctx, model.CleanupRun{PolicyKey: "k"})),
		"run":              sqltest.Err(cs.Run(ctx, 1)),
		"run metadata":     sqltest.Err(cs.RunMetadata(ctx, 1)),
		"update run":       cs.UpdateRun(ctx, model.CleanupRun{ID: 1}),
		"last run":         sqltest.Err(cs.LastRun(ctx, "k")),
		"run page":         sqltest.Err(cs.RunPage(ctx, port.RunListing{Limit: 25})),
		"report totals":    sqltest.Err(cs.OutcomeTotals(ctx, 1)),
		"create execution": cs.CreateExecution(ctx, execution),
		"execution":        sqltest.Err(cs.Execution(ctx, 1)),
		"occurrence":       sqltest.Err(cs.ExecutionForOccurrence(ctx, "fire")),
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
