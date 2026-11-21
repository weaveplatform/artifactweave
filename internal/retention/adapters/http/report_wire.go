package retentionhttp

import (
	"fmt"
	"strconv"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func coveredProjects(p model.CleanupPolicy) []string {
	if p.ProjectKey != "" {
		return []string{p.ProjectKey}
	}
	return kernel.Clone(p.Criteria.IncludedProjects)
}

func runWire(s model.CleanupRunSnapshot, content int64) (artgen.CleanupPolicyRunViewEntry, error) {
	r, p := s.Run, s.Policy
	id, err := cleanup.PublicRunID(r.TriggerID)
	if err != nil {
		return artgen.CleanupPolicyRunViewEntry{}, fmt.Errorf("cleanup run response identity: %w", err)
	}
	out := artgen.CleanupPolicyRunViewEntry{
		RunId: &id, PolicyKey: &r.PolicyKey, ItemType: httpkit.PtrTo(artgen.CleanupPolicyRunViewEntryItemType(r.ItemType)),
		Status: httpkit.PtrTo(artgen.CleanupPolicyRunViewEntryStatus(r.Status)), DryRun: &r.DryRun, Manual: &r.Manual,
		TriggeredBy: httpkit.PtrTo(r.DisplayActor()), TotalItems: &r.TotalItems, CleanedItems: &r.CleanedItems, CleanedSize: &content,
		DisplayProject: optionalString(s.DisplayProject), IncludedProjects: optionalList(coveredProjects(p)),
	}
	if !r.Started.IsZero() {
		out.StartTime = httpkit.PtrTo(r.Started.UnixMilli())
	}
	if !r.Ended.IsZero() {
		out.EndTime = httpkit.PtrTo(r.Ended.UnixMilli())
	}
	return out, nil
}

func summaryWire(report cleanup.RunReport) (artgen.CleanupRunSummary, error) {
	r, p, t := report.Snapshot.Run, report.Snapshot.Policy, report.Totals
	id, err := cleanup.PublicRunID(r.TriggerID)
	if err != nil {
		return artgen.CleanupRunSummary{}, fmt.Errorf("cleanup summary identity: %w", err)
	}
	processed, virtual, content := t.Deleted, t.DeletedVirtualBytes, t.DeletedContentBytes
	if r.DryRun {
		processed, virtual, content = t.Selected, t.SelectedVirtualBytes, t.SelectedContentBytes
	}
	execution := artgen.CleanupExecutionSummary{StartedAt: optionalTime(r.Started), FinishedAt: optionalTime(r.Ended)}
	end := r.Ended
	if end.IsZero() {
		end = report.ObservedAt
	}
	if !r.Started.IsZero() && !end.Before(r.Started) {
		execution.DurationMillis = httpkit.PtrTo(end.Sub(r.Started).Milliseconds())
	}
	schedule := artgen.CleanupSchedule{CronExpression: &p.CronExp}
	if p.DurationMinutes > 0 {
		schedule.MaxRunDurationMillis = httpkit.PtrTo(int64(p.DurationMinutes) * 60 * 1000)
	}
	criteria := artgen.CleanupRunCriteria{KeepLastNVersions: httpkit.PtrTo(int64(p.Criteria.KeepLastNVersions))}
	if days := p.Criteria.CreatedBeforeInDays; days > 0 {
		criteria.CreatedBefore = httpkit.PtrTo(strconv.Itoa(days) + " days")
	} else if months := p.Criteria.CreatedBeforeInMonths; months > 0 {
		criteria.CreatedBefore = httpkit.PtrTo(strconv.Itoa(months) + " months")
	}
	if days := p.Criteria.LastDownloadedBeforeInDays; days > 0 {
		criteria.LastDownloadedBeforeInDays = httpkit.PtrTo(int64(days))
	} else if months := p.Criteria.LastDownloadedBeforeInMonths; months > 0 {
		// Calendar-month criteria use the run's frozen selection clock, not
		// a guessed 30-day multiplier or the time of this report request.
		at := report.SelectedAt.UTC()
		days := (at.Unix() - at.AddDate(0, -months, 0).Unix()) / (24 * 60 * 60)
		criteria.LastDownloadedBeforeInDays = &days
	}
	return artgen.CleanupRunSummary{
		PolicySummary: &artgen.CleanupPolicySummary{
			PolicyName:  &p.Key,
			RunId:       &id,
			DryRun:      &r.DryRun,
			Status:      httpkit.PtrTo(string(r.Status)),
			TriggeredBy: httpkit.PtrTo(r.DisplayActor()),
		},
		ExecutionSummary: &execution,
		ProcessedItemsSummary: &artgen.CleanupProcessedItemsSummary{
			ItemType:                   httpkit.PtrTo(string(r.ItemType)),
			TotalItems:                 &r.TotalItems,
			ProcessedItems:             &processed,
			FailedItems:                &t.Failed,
			ProcessedVirtualSizeBytes:  &virtual,
			ProcessedPhysicalSizeBytes: &content,
		},
		RunConfiguration: &artgen.CleanupRunConfiguration{
			CreatedAt: optionalTime(
				p.Created,
			),
			CreatedBy:       &p.CreatedBy,
			ModifiedAt:      optionalTime(p.Updated),
			ModifiedBy:      &p.UpdatedBy,
			Description:     &p.Description,
			SkipTrashCan:    &p.SkipTrashcan,
			Schedule:        &schedule,
			CleanupCriteria: &criteria,
			Target: &artgen.CleanupTarget{
				ReposIncluded:      optionalList(p.Criteria.Repos),
				ReposExcluded:      optionalList(p.Criteria.ExcludedRepos),
				PackageTypes:       optionalList(p.Criteria.PackageTypes),
				PackagesIncluded:   optionalList(p.Criteria.IncludedPackages),
				PackagesExcluded:   optionalList(p.Criteria.ExcludedPackages),
				IncludeAllProjects: &p.Criteria.IncludeAllProjects,
				ProjectsIncluded:   optionalList(coveredProjects(p)),
			},
		},
	}, nil
}

func optionalTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

func policyViewWire(view cleanup.PolicyOverview) (artgen.CleanupPolicyEntry, error) {
	p := view.Policy
	out := artgen.CleanupPolicyEntry{
		PolicyKey:        &p.Key,
		Enabled:          &p.Enabled,
		ItemType:         httpkit.PtrTo(string(p.ItemType)),
		DisplayProject:   optionalString(view.DisplayProject),
		IncludedProjects: optionalList(coveredProjects(p)),
	}
	if !view.Next.IsZero() {
		out.NextRunTime = httpkit.PtrTo(view.Next.UnixMilli())
	}
	if view.Last != nil {
		r := view.Last.Run
		id, err := cleanup.PublicRunID(r.TriggerID)
		if err != nil {
			return artgen.CleanupPolicyEntry{}, fmt.Errorf("cleanup overview identity: %w", err)
		}
		out.LastRunData = &artgen.CleanupPolicyLastRun{
			RunId:            &id,
			DryRun:           &r.DryRun,
			Status:           httpkit.PtrTo(string(r.Status)),
			TotalItems:       &r.TotalItems,
			ReportDownloaded: &r.ReportDownloaded,
		}
		if !r.Started.IsZero() {
			out.LastRunData.LastRunTime = httpkit.PtrTo(r.Started.UnixMilli())
		}
	}
	return out, nil
}
