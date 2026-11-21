package retentionhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupRunViewWithoutRouterKeepsDecodedFiltersInLink(t *testing.T) {
	db := inmem.New()
	ctx := t.Context()
	s := PackagePoliciesAPI{Policies: &cleanup.Service{
		Store: db, Guard: db.Projects().Guard,
		Authorize: func(ctx context.Context) (cleanup.Policy, error) { return fullPolicy{}, db.Projects().Guard(ctx) },
	}}
	p := model.CleanupPolicy{Key: "query-policy", Revision: "snapshot-revision", ProjectKey: "alpha", ItemType: model.CleanupPackage}
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ids := []int64{}
	for i := range 2 {
		r, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{
			PolicyKey: p.Key, ItemType: p.ItemType, Status: api.RunCompleted, TotalItems: 1, Started: at.Add(time.Duration(i) * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		r.TriggerID = r.ID
		if err := db.Cleanup().UpdateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := db.Cleanup().CreateExecution(ctx, model.CleanupExecution{
			RunID: r.ID, Policy: p, At: at, ProjectIncarnation: "original-alpha", Credential: kernel.CredentialReference{Name: "admin", TokenID: "verified"},
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	params := artgen.GetCleanupPolicyRunsViewParams{
		PolicyKey: &p.Key, ProjectKey: &p.ProjectKey, Page: httpkit.PtrTo(1), PageSize: httpkit.PtrTo(1),
		From: httpkit.PtrTo(at.Add(-time.Minute).UnixMilli()), To: httpkit.PtrTo(at.Add(3 * time.Hour).UnixMilli()),
		ItemTypes: httpkit.PtrTo([]string{"package", "build"}), Status: httpkit.PtrTo([]string{"COMPLETED", "FAILED"}),
		ProjectKeyList: httpkit.PtrTo([]string{"alpha", "beta"}), ExcludeBlankExecutions: httpkit.PtrTo("true"),
		SortBy: httpkit.PtrTo(artgen.GetCleanupPolicyRunsViewParamsSortBy("runId")), SortOrder: httpkit.PtrTo(artgen.GetCleanupPolicyRunsViewParamsSortOrderAsc),
	}
	response, err := s.GetCleanupPolicyRunsView(ctx, artgen.GetCleanupPolicyRunsViewRequestObject{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	first := response.(artgen.GetCleanupPolicyRunsView200JSONResponse)
	if first.Body.RunViews == nil || len(*first.Body.RunViews) != 1 || httpkit.Deref((*first.Body.RunViews)[0].RunId) != int(ids[0]) || first.Headers.Link == nil {
		t.Fatal("decoded first-page request lost its row or continuation")
	}
	target, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(*first.Headers.Link, "<"), `>; rel="next"`))
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	if target.Path != cleanupRunViewPath || query.Get("page") != "2" || query.Get("pageSize") != "1" || query.Get("policyKey") != p.Key || query.Get("projectKey") != "alpha" ||
		strings.Join(query["status"], ",") != "COMPLETED,FAILED" || strings.Join(query["itemTypes"], ",") != "package,build" ||
		strings.Join(query["projectKeyList"], ",") != "alpha,beta" || query.Get("from") == "" || query.Get("to") == "" || query.Get("excludeBlankExecutions") != "true" ||
		query.Get("sortBy") != "runId" || query.Get("sortOrder") != "asc" {
		t.Fatal("decoded continuation dropped a selection or order constraint", target)
	}
	params.Page = httpkit.PtrTo(2)
	response, err = s.GetCleanupPolicyRunsView(ctx, artgen.GetCleanupPolicyRunsViewRequestObject{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	last := response.(artgen.GetCleanupPolicyRunsView200JSONResponse)
	if last.Headers.Link != nil || last.Body.RunViews == nil || len(*last.Body.RunViews) != 1 || httpkit.Deref((*last.Body.RunViews)[0].RunId) != int(ids[1]) {
		t.Fatal("decoded continuation failed to terminate on its second row")
	}
	if !KeepsSearchQuery(&http.Request{Method: http.MethodGet, URL: target}) || KeepsSearchQuery(&http.Request{Method: http.MethodPost, URL: target}) ||
		KeepsSearchQuery(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/different"}}) {
		t.Fatal("query retention widened beyond the run-view GET")
	}
}

func TestUnitCleanupReportWireUsesFrozenRuleAndTruthfulSizes(t *testing.T) {
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	p := model.CleanupPolicy{
		Key:             "policy",
		ProjectKey:      "alpha",
		Description:     "frozen rule",
		Created:         at,
		Updated:         at,
		CreatedBy:       "creator",
		UpdatedBy:       "editor",
		CronExp:         "0 0 2 * * ?",
		DurationMinutes: 2,
		ExecutionCredential: kernel.CredentialReference{
			Name:    "private-actor",
			TokenID: "do-not-render",
		},
		Criteria: model.CleanupCriteria{
			PackageTypes:                 []string{"npm"},
			Repos:                        []string{"npm-local"},
			IncludedPackages:             []string{"**"},
			CreatedBeforeInMonths:        2,
			LastDownloadedBeforeInMonths: 1,
			KeepLastNVersions:            3,
		},
	}
	r := model.CleanupRun{
		ID:           7,
		TriggerID:    7,
		PolicyKey:    p.Key,
		ItemType:     model.CleanupPackage,
		Status:       api.RunCompletedWithErrors,
		Started:      at,
		Ended:        at.Add(time.Minute),
		TotalItems:   3,
		CleanedItems: 2,
		CleanedSize:  10,
		TriggeredBy:  "enabling-admin",
		Report:       []byte("do-not-render"),
	}
	report := cleanup.RunReport{
		Snapshot:   model.CleanupRunSnapshot{Run: r, Policy: p, DisplayProject: "Original Alpha"},
		SelectedAt: at,
		ObservedAt: at.AddDate(0, 1, 0),
		Totals:     model.CleanupOutcomeTotals{Items: 3, Deleted: 2, Failed: 1, DeletedVirtualBytes: 10, DeletedContentBytes: 5},
	}
	out, err := summaryWire(report)
	if err != nil {
		t.Fatal(err)
	}
	if *out.PolicySummary.TriggeredBy != "system" || *out.ProcessedItemsSummary.ProcessedItems != 2 || *out.ProcessedItemsSummary.ProcessedVirtualSizeBytes != 10 ||
		*out.ProcessedItemsSummary.ProcessedPhysicalSizeBytes != 5 ||
		*out.ProcessedItemsSummary.FailedItems != 1 {
		t.Fatal("summary invented actor, counts or storage effects")
	}
	if *out.ExecutionSummary.DurationMillis != 60000 || *out.RunConfiguration.CleanupCriteria.CreatedBefore != "2 months" ||
		*out.RunConfiguration.CleanupCriteria.LastDownloadedBeforeInDays != 28 ||
		*out.RunConfiguration.Schedule.MaxRunDurationMillis != 120000 {
		t.Fatal("summary recalculated frozen criteria at request time")
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "do-not-render") || strings.Contains(string(body), "private-actor") {
		t.Fatal("summary serialized private job state")
	}
	view, err := runWire(report.Snapshot, 5)
	if err != nil {
		t.Fatal(err)
	}
	if *view.CleanedSize != 5 || *view.CleanedItems != 2 || *view.RunId != 7 || *view.DisplayProject != "Original Alpha" || len(*view.IncludedProjects) != 1 {
		t.Fatal("run response lost identity/scope or used virtual size")
	}
	report.Snapshot.Run.DryRun = true
	report.Snapshot.Run.CleanedItems = 0
	report.Snapshot.Run.CleanedSize = 0
	report.Totals = model.CleanupOutcomeTotals{Items: 3, Selected: 2, Skipped: 1, SelectedVirtualBytes: 10, SelectedContentBytes: 5}
	out, err = summaryWire(report)
	if err != nil {
		t.Fatal(err)
	}
	if *out.ProcessedItemsSummary.ProcessedItems != 2 || *out.ProcessedItemsSummary.ProcessedVirtualSizeBytes != 10 || *out.ProcessedItemsSummary.ProcessedPhysicalSizeBytes != 5 ||
		!*out.PolicySummary.DryRun {
		t.Fatal("dry report presented selected bytes as deletion effects")
	}
}
