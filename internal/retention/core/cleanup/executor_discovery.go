package cleanup

import (
	"context"
	"fmt"
	"math"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

const executionPageSize = 64

func afterRepository(p *model.CleanupProgress) {
	p.PackageAfter, p.VersionAfter, p.FileAfter = "", "", ""
	if p.RepositoryDone {
		p.Phase = model.CleanupFinished
	} else {
		p.Phase = model.CleanupRepositories
	}
}

func afterPackage(p *model.CleanupProgress) {
	p.VersionAfter, p.FileAfter = "", ""
	if p.PackageDone {
		afterRepository(p)
	} else {
		p.Phase = model.CleanupPackages
	}
}

func afterVersion(p *model.CleanupProgress) {
	p.FileAfter, p.Reason = "", ""
	if p.VersionDone {
		afterPackage(p)
	} else {
		p.Phase = model.CleanupVersions
	}
}

func (x Executor) settleTraversal(r *model.CleanupRun, p model.CleanupProgress) {
	if p.Phase != model.CleanupFinished {
		return
	}
	r.Status, r.Ended = api.RunCompleted, x.Policies.now()
	if p.HadErrors {
		r.Status = api.RunCompletedWithErrors
	}
}

func (x Executor) discoverStep(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, s *Selection, fence func(context.Context) error) error {
	next := p
	switch p.Phase {
	case model.CleanupRepositories:
		rows, cursor, err := s.Repositories(ctx, kernel.Page{Limit: 1, After: p.RepositoryAfter})
		if err != nil {
			return err
		}
		next.RepositoryAfter, next.RepositoryDone = cursor, cursor == ""
		if len(rows) == 0 {
			if cursor == "" {
				next.Phase = model.CleanupFinished
			}
		} else {
			next.Repository = rows[0]
			next.PackageAfter = ""
			next.Phase = model.CleanupPackages
		}
	case model.CleanupPackages:
		rows, cursor, err := s.Packages(ctx, p.Repository, kernel.Page{Limit: 1, After: p.PackageAfter})
		if err != nil {
			return err
		}
		next.PackageAfter, next.PackageDone = cursor, cursor == ""
		if len(rows) == 0 {
			if cursor == "" {
				afterRepository(&next)
			}
		} else {
			next.Package = rows[0]
			next.VersionAfter = ""
			next.Phase = model.CleanupVersions
		}
	case model.CleanupVersions:
		rows, cursor, err := s.Versions(ctx, p.Package, kernel.Page{Limit: 1, After: p.VersionAfter})
		if err != nil {
			return err
		}
		next.VersionAfter, next.VersionDone = cursor, cursor == ""
		if len(rows) == 0 {
			if cursor == "" {
				afterPackage(&next)
			}
		} else {
			next.Version = rows[0]
			next.FileAfter = ""
			next.Phase = model.CleanupCandidates
		}
	case model.CleanupCandidates:
		return x.discoverCandidates(ctx, e, p, s, fence)
	case model.CleanupFinished:
		return fmt.Errorf("unfinished run has finished continuation: %w", kernel.ErrConflict)
	default:
		return fmt.Errorf("unknown cleanup continuation: %w", kernel.ErrStore)
	}
	return x.publishDiscovery(ctx, e, p, next, nil, fence)
}

func (x Executor) discoverCandidates(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, s *Selection, fence func(context.Context) error) error {
	page, err := s.Candidates(ctx, p.Version, kernel.Page{Limit: executionPageSize, After: p.FileAfter})
	if err != nil {
		return err
	}
	next := p
	entries := make([]model.CleanupEntry, 0, len(page.Items)+1)
	if p.FileAfter == "" {
		next.Version = page.Version
	}
	if p.FileAfter != "" && !page.Version.LastDownloaded.Equal(p.Version.LastDownloaded) {
		page.Reason, page.Items = ReasonChanged, nil
	}
	if page.Reason != ReasonSelected {
		if p.FileAfter != "" {
			next.Phase, next.Reason = model.CleanupSkipping, string(page.Reason)
		} else {
			entries = append(entries, versionDecision(e.RunID, page.Version, model.CleanupSkipped, string(page.Reason)))
			afterVersion(&next)
		}
	} else {
		if p.FileAfter == "" {
			entries = append(entries, versionDecision(e.RunID, page.Version, model.CleanupSelected, string(ReasonSelected)))
		}
		for _, file := range page.Items {
			entries = append(
				entries,
				model.CleanupEntry{
					RunID:     e.RunID,
					EntityID:  file.ID,
					VersionID: page.Version.ID,
					Revision:  1,
					Kind:      model.CleanupItemEntry,
					State:     model.CleanupPending,
					Snapshot:  file,
					Reason:    string(ReasonSelected),
				},
			)
		}
		next.FileAfter = page.Next
		if page.Next == "" {
			next.Phase = model.CleanupRemoving
		}
	}
	return x.publishDiscovery(ctx, e, p, next, entries, fence)
}

func (x Executor) publishDiscovery(
	ctx context.Context,
	e model.CleanupExecution,
	p, next model.CleanupProgress,
	entries []model.CleanupEntry,
	fence func(context.Context) error,
) error {
	return x.publish(ctx, e, p, fence, true, func(ctx context.Context, r *model.CleanupRun, current *model.CleanupProgress) error {
		if next.Phase == model.CleanupSkipping {
			if err := x.versionOutcome(ctx, e, p, model.CleanupSkipped, next.Reason); err != nil {
				return err
			}
		}
		for _, entry := range entries {
			if r.DryRun && entry.Kind == model.CleanupItemEntry {
				entry.State = model.CleanupSelected
			}
			created, err := x.Store.Cleanup().CreateEntry(ctx, entry)
			if err != nil {
				return fmt.Errorf("record cleanup selection: %w", err)
			}
			if created && entry.Kind == model.CleanupItemEntry {
				if r.TotalItems == math.MaxInt64 {
					return fmt.Errorf("cleanup selected total overflow: %w", kernel.ErrStore)
				}
				r.TotalItems++
			}
		}
		if r.DryRun && next.Phase == model.CleanupRemoving {
			afterVersion(&next)
		}
		next.Revision = current.Revision
		*current = next
		x.settleTraversal(r, next)
		return nil
	})
}

func versionDecision(run int64, v artifactsapi.IndexVersion, state model.CleanupEntryState, reason string) model.CleanupEntry {
	return model.CleanupEntry{
		RunID:     run,
		EntityID:  v.ID,
		VersionID: v.ID,
		Revision:  1,
		Kind:      model.CleanupVersionEntry,
		State:     state,
		Snapshot:  artifactsapi.IndexFile{Version: v},
		Reason:    reason,
	}
}
