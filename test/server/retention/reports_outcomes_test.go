package retention_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccCleanupReportOutcomesReference(t *testing.T) {
	cleanupReportOutcomes(t, func(*testing.T) harness.Store { return inmem.New() })
}

func TestAccCleanupReportOutcomesSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t, appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			cleanupReportOutcomes(t, func(t *testing.T) harness.Store { return newStore(t) })
		},
		sqltest.WithPostgres(), sqltest.WithKeyring(keys),
	)
}

func cleanupReportOutcomes(t *testing.T, newStore func(*testing.T) harness.Store) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		status  retapi.RunStatus
		outcome string
		failed  int64
	}{
		{"stop", retapi.RunStopped, "pending", 0},
		{"force-disable", retapi.RunStopped, "pending", 0},
		{"delete-denied", retapi.RunCompletedWithErrors, "failed", 1},
		{"retries-exhausted", retapi.RunFailed, "failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newStore(t)
			f := newCleanupExecutorFixture(t, db)
			w := policyWorld{t: t, store: db, h: f.handler}
			run, work := f.selected(t)
			fence := func(ctx context.Context) error {
				_, err := db.WorkQueue().GuardLease(ctx, work.Kind, work.Key, work.Generation)
				return err
			}
			_, err := f.executor.Step(f.ctx, run.ID, fence)
			storekit.Must(t, err)
			partial := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(f.ctx, run.ID))
			if partial.CleanedItems != 1 || partial.Status.Terminal() {
				t.Fatal("fixture did not retain one partial committed effect")
			}
			id := strconv.FormatInt(run.ID, 10)
			switch tc.name {
			case "stop":
				w.request(http.MethodPut, cleanupReports+id, "admin", `{"action":"stop"}`, http.StatusAccepted)
			case "force-disable":
				w.request(http.MethodPut, "/artifactory/api/cleanup/policies/forceDisableAll", "admin", "", http.StatusOK)
			case "delete-denied":
				f.executor.Removal = cleanupDeniedItem{ItemRemoval: f.executor.Removal, id: f.ids[1]}
			case "retries-exhausted":
				f.executor.Removal = cleanupPersistentItemFault{ItemRemoval: f.executor.Removal, id: f.ids[1]}
				work.FinalAttempt = true
			}
			storekit.Must(t, f.executor.Execute(f.ctx, work))
			finished := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(f.ctx, run.ID))
			if finished.Status != tc.status || finished.CleanedItems != 1 {
				t.Fatal("fixture did not settle truthful partial outcome", finished.Status, finished.CleanedItems)
			}
			response := w.request(http.MethodGet, cleanupReports+"summaryReports?policyKey=execution-policy&triggerId="+id, "admin", "", http.StatusOK)
			var summary artgen.CleanupRunSummary
			storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &summary))
			if summary.PolicySummary == nil || httpkit.Deref(summary.PolicySummary.Status) != string(tc.status) || summary.ProcessedItemsSummary == nil ||
				httpkit.Deref(summary.ProcessedItemsSummary.ProcessedItems) != 1 || httpkit.Deref(summary.ProcessedItemsSummary.FailedItems) != tc.failed {
				t.Fatal("summary invented successful outcomes or lost retained failures")
			}
			response = w.request(http.MethodGet, cleanupReports+"downloadReport?policyKey=execution-policy&runId="+id, "admin", "", http.StatusOK)
			rows, err := csv.NewReader(response.Body).ReadAll()
			storekit.Must(t, err)
			counts := map[string]int{}
			for _, row := range rows[1:] {
				counts[row[4]]++
			}
			if len(rows) != 3 || counts["deleted"] != 1 || counts[tc.outcome] != 1 {
				t.Fatal("CSV lost attempted/unattempted outcomes or included version decisions", counts)
			}
		})
	}
}
