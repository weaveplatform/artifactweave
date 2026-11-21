package retentionhttp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

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
