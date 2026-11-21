package retention_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const cleanupReports = "/artifactory/api/cleanup/policies/runs/"

func checkNativeCleanupReport(t *testing.T, f *cleanupExecutorFixture, run model.CleanupRun) {
	t.Helper()
	w := policyWorld{t: t, store: f.db, h: f.handler}
	id := strconv.FormatInt(run.ID, 10)
	response := w.request(http.MethodGet, cleanupReports+"downloadReport?policyKey=execution-policy&runId="+id, "admin", "", http.StatusOK)
	rows, err := csv.NewReader(response.Body).ReadAll()
	storekit.Must(t, err)
	if len(rows) != 3 {
		t.Fatal("native report lost actual file outcomes", len(rows))
	}
	for _, row := range rows[1:] {
		if row[0] != "execution-maven" || !strings.HasPrefix(row[1], "com/acme/app/1.0/app-1.0.") || row[2] != "10" || row[3] == "" || row[4] != "deleted" {
			t.Fatal("native report substituted current items or protected version", row)
		}
	}
}

func TestAccCleanupReportsReference(t *testing.T) { cleanupReportsContract(t, inmem.New()) }

func TestAccCleanupReportsSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) { cleanupReportsContract(t, newStore(t)) },
		sqltest.WithPostgres(), sqltest.WithKeyring(keys),
	)
}

func TestAccCleanupReportsUnavailableStoreReturns501(t *testing.T) {
	handler := harness.Router(harness.Config{})
	for _, tc := range []struct{ name, path string }{
		{"exact run", cleanupReports + "policy/1"},
		{"run view", cleanupReports + "view"},
		{"summary", cleanupReports + "summaryReports?policyKey=policy&triggerId=1"},
		{"policy view", "/artifactory/api/cleanup/policies/view?itemTypes=package"},
		{"CSV", cleanupReports + "downloadReport?policyKey=policy&runId=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := support.Do(t, handler, http.MethodGet, tc.path, nil, "")
			if response.Code != http.StatusNotImplemented {
				t.Fatal("unavailable report binding changed its answer", response.Code)
			}
		})
	}
}

func cleanupReportRun(t *testing.T, f *cleanupExecutorFixture, dry bool) model.CleanupRun {
	t.Helper()
	return cleanupPolicyReportRun(t, f, "execution-policy", dry)
}

func cleanupPolicyReportRun(t *testing.T, f *cleanupExecutorFixture, key string, dry bool) model.CleanupRun {
	t.Helper()
	w := policyWorld{t: t, store: f.db, h: f.handler}
	response := w.request(http.MethodPut, policies+"/"+key+"/run?dryRun="+strconv.FormatBool(dry), "admin", "", http.StatusAccepted)
	var trigger artgen.CleanupRunTrigger
	storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &trigger))
	if trigger.TriggerId == nil || *trigger.TriggerId <= 0 {
		t.Fatal("HTTP cleanup admission lost its trigger")
	}
	id := int64(*trigger.TriggerId)
	item, taken, err := f.db.WorkQueue().
		LeaseKey(f.ctx, cleanup.WorkKind, strconv.FormatInt(id, 10), work.WorkLease{Holder: "report-worker", Now: f.now, Until: f.now.Add(time.Hour)})
	storekit.Must(t, err)
	if !taken {
		t.Fatal("HTTP run was not durably queued")
	}
	storekit.Must(t, f.executor.Execute(f.ctx, item))
	storekit.Must(t, f.db.WorkQueue().Complete(f.ctx, item.Kind, item.Key, item.Generation, f.now))
	run := storekit.Get[model.CleanupRun](t)(f.db.Cleanup().Run(f.ctx, id))
	if run.Status != retapi.RunCompleted {
		t.Fatal("real cleanup report fixture did not finish", run.Status)
	}
	return run
}

func cleanupReportsContract(t *testing.T, db harness.Store) {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	storekit.Must(t, db.Users().Create(f.ctx, accesstest.User{Name: "report-reader", PasswordHash: support.CheapHash}))
	w := policyWorld{t: t, store: db, h: f.handler}
	dry := cleanupReportRun(t, f, true)
	check := func(run model.CleanupRun) {
		t.Helper()
		id := strconv.FormatInt(run.ID, 10)
		query := "policyKey=execution-policy&triggerId=" + id
		response := w.request(http.MethodGet, cleanupReports+"summaryReports?"+query, "admin", "", http.StatusOK)
		var summary artgen.CleanupRunSummary
		storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &summary))
		if summary.PolicySummary == nil || httpkit.Deref(summary.PolicySummary.RunId) != int(run.ID) || httpkit.Deref(summary.PolicySummary.Status) != string(run.Status) ||
			summary.ProcessedItemsSummary == nil {
			t.Fatal("summary lost exact persisted run identity/status")
		}
		processed := summary.ProcessedItemsSummary
		if httpkit.Deref(processed.ProcessedItems) != 2 || httpkit.Deref(processed.ProcessedVirtualSizeBytes) != int64(len("real-one.bin")+len("real-two.bin")) ||
			httpkit.Deref(processed.ProcessedPhysicalSizeBytes) != httpkit.Deref(processed.ProcessedVirtualSizeBytes) {
			t.Fatal("summary confused dry selection and actual outcomes")
		}
		response = w.request(http.MethodGet, cleanupReports+"execution-policy/"+id, "admin", "", http.StatusOK)
		var exact artgen.CleanupPolicyRunViewEntry
		storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &exact))
		wantDeleted := int64(2)
		if run.DryRun {
			wantDeleted = 0
		}
		if httpkit.Deref(exact.RunId) != int(run.ID) || httpkit.Deref(exact.CleanedItems) != wantDeleted || httpkit.Deref(exact.DryRun) != run.DryRun {
			t.Fatal("exact lookup substituted another run or invented dry effects")
		}
		response = w.request(http.MethodGet, cleanupReports+"downloadReport?policyKey=execution-policy&runId="+id, "admin", "", http.StatusOK)
		if response.Header().Get("Content-Type") != "application/octet-stream" || response.Header().Get("Content-Disposition") != `attachment; filename="cleanup-`+id+`.csv"` {
			t.Fatal("download lost its authored media type or safe file name")
		}
		rows, err := csv.NewReader(response.Body).ReadAll()
		storekit.Must(t, err)
		if len(rows) != 3 || len(rows[0]) != 6 || rows[0][0] != "repository" {
			t.Fatal("CSV did not contain one row per retained item", rows)
		}
		wantOutcome := "deleted"
		if run.DryRun {
			wantOutcome = "selected"
		}
		for _, row := range rows[1:] {
			if row[0] != "execution" || row[2] != "12" || row[4] != wantOutcome || row[3] == "" {
				t.Fatal("CSV disagrees with retained item facts", row)
			}
		}
		stored := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(f.ctx, run.ID))
		if !stored.ReportDownloaded || stored.CleanedItems != run.CleanedItems || stored.CleanedSize != run.CleanedSize {
			t.Fatal("download changed execution facts or lost completion marker")
		}
	}
	check(dry)
	f.preserved(t)
	actual := cleanupReportRun(t, f, false)
	check(actual)
	response := w.request(http.MethodGet, cleanupReports+"view?policyKey=execution-policy&pageSize=1&sortBy=runId&sortOrder=desc", "admin", "", http.StatusOK)
	var page artgen.CleanupPolicyRunView
	storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &page))
	if page.RunViews == nil || len(*page.RunViews) != 1 || httpkit.Deref((*page.RunViews)[0].RunId) != int(actual.ID) || page.Pagination == nil ||
		httpkit.Deref(page.Pagination.Total) != 2 {
		t.Fatal("run view did not apply stable paging/counts")
	}
	response = w.request(http.MethodGet, "/artifactory/api/cleanup/policies/view?itemTypes=package&policyKey=execution-policy", "admin", "", http.StatusOK)
	var policiesView artgen.CleanupPolicyView
	storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &policiesView))
	if policiesView.Policies == nil || len(*policiesView.Policies) != 1 || (*policiesView.Policies)[0].LastRunData == nil ||
		!httpkit.Deref((*policiesView.Policies)[0].LastRunData.ReportDownloaded) {
		t.Fatal("policy overview lost actual last-run download state")
	}
	w.request(http.MethodDelete, policies+"/execution-policy", "admin", "", http.StatusOK)
	check(actual)
	check(dry)
	for _, tc := range []struct {
		name, path, user string
		status           int
	}{
		{"anonymous", cleanupReports + "execution-policy/" + strconv.FormatInt(actual.ID, 10), "", http.StatusUnauthorized},
		{"non-administrator", cleanupReports + "summaryReports?policyKey=execution-policy&triggerId=" + strconv.FormatInt(actual.ID, 10), "report-reader", http.StatusForbidden},
		{"wrong policy", cleanupReports + "different-policy/" + strconv.FormatInt(actual.ID, 10), "admin", http.StatusNotFound},
		{"missing report", cleanupReports + "downloadReport?policyKey=execution-policy&runId=999999", "admin", http.StatusNotFound},
		{"invalid page", cleanupReports + "view?page=0", "admin", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := policyWorld{t: t, store: db, h: f.handler}
			world.request(http.MethodGet, tc.path, tc.user, "", tc.status)
		})
	}
	checkReportActorOrdering(t, f, dry.ID)
}

func checkReportActorOrdering(t *testing.T, f *cleanupExecutorFixture, original int64) {
	t.Helper()
	cs := f.db.Cleanup()
	execution := storekit.Get[model.CleanupExecution](t)(cs.Execution(f.ctx, original))
	p := execution.Policy
	p.Key = "report-actor-order"
	storekit.Must(t, cs.CreatePolicy(f.ctx, p))
	ids := []int64{}
	for _, actor := range []struct {
		name   string
		manual bool
	}{{name: "alice"}, {name: "bob", manual: true}, {name: "zulu"}} {
		run := storekit.Get[model.CleanupRun](t)(cs.CreateRun(f.ctx, model.CleanupRun{
			PolicyKey: p.Key, ItemType: model.CleanupPackage, Status: retapi.RunCompleted, TriggeredBy: actor.name, Manual: actor.manual,
		}))
		run.TriggerID = run.ID
		storekit.Must(t, cs.UpdateRun(f.ctx, run))
		e := execution
		e.RunID, e.Occurrence, e.Policy = run.ID, "", p
		storekit.Must(t, cs.CreateExecution(f.ctx, e))
		ids = append(ids, run.ID)
	}
	for _, order := range []struct {
		name   string
		ids    []int64
		actors []string
	}{
		{name: "asc", ids: []int64{ids[1], ids[0], ids[2]}, actors: []string{"bob", "system", "system"}},
		{name: "desc", ids: []int64{ids[2], ids[0], ids[1]}, actors: []string{"system", "system", "bob"}},
	} {
		for index, wantID := range order.ids {
			w := policyWorld{t: t, store: f.db, h: f.handler}
			response := w.request(
				http.MethodGet,
				cleanupReports+"view?policyKey="+p.Key+"&pageSize=1&sortBy=triggeredBy&sortOrder="+order.name+"&page="+strconv.Itoa(index+1),
				"admin",
				"",
				http.StatusOK,
			)
			var page artgen.CleanupPolicyRunView
			storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &page))
			if page.Pagination == nil || httpkit.Deref(page.Pagination.Total) != 3 || page.RunViews == nil || len(*page.RunViews) != 1 {
				t.Fatal("actor sorting lost its bounded window or total")
			}
			row := (*page.RunViews)[0]
			if httpkit.Deref(row.RunId) != int(wantID) || httpkit.Deref(row.TriggeredBy) != order.actors[index] {
				t.Fatal("actor page sorted private enabling callers instead of the public actor", order.name, index)
			}
		}
	}
}
