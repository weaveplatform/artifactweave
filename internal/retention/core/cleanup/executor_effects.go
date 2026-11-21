package cleanup

import (
	"context"
	"errors"
	"fmt"
	"math"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func sameVersionRevision(a, b artifactsapi.IndexVersion) bool {
	return a.ID == b.ID && a.MutationRevision == b.MutationRevision && a.LeadRevision == b.LeadRevision
}

// The owner locks this version row and every linked mutation replaces its
// revision in that same transaction. This checks the complete selected set
// without walking all current and saved files again for every removal.
func (x Executor) unchangedVersion(ctx context.Context, p model.CleanupProgress, s *Selection) error {
	v, err := s.currentVersion(ctx, p.Version)
	if err != nil {
		return err
	}
	if v.MutationRevision == "" || v.LeadRevision == "" || !sameVersionRevision(v, p.Version) ||
		v.Protected || !v.LastDownloaded.Equal(p.Version.LastDownloaded) || s.criteria.identityReason(versionIdentity(v)) != ReasonSelected {
		return fmt.Errorf("cleanup version snapshot changed: %w", kernel.ErrConflict)
	}
	return nil
}

func (x Executor) removeStep(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, s *Selection, fence func(context.Context) error) error {
	entry, err := x.Store.Cleanup().PendingEntry(ctx, e.RunID, p.Version.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		return x.publish(ctx, e, p, fence, true, func(_ context.Context, r *model.CleanupRun, p *model.CleanupProgress) error {
			afterVersion(p)
			x.settleTraversal(r, *p)
			return nil
		})
	}
	if err != nil {
		return fmt.Errorf("read next cleanup intent: %w", err)
	}
	_, err = x.Removal.Remove(ctx, artifactsapi.RemovalRequest{Credential: e.Credential, Item: entry.Snapshot, Properties: s.properties, SkipTrashcan: e.Policy.SkipTrashcan},
		func(ctx context.Context) error { return x.unchangedVersion(ctx, p, s) },
		func(ctx context.Context, result artifactsapi.RemovalResult) error {
			return x.publish(ctx, e, p, fence, true, func(ctx context.Context, r *model.CleanupRun, next *model.CleanupProgress) error {
				if err := x.acknowledge(ctx, e.RunID, p.Version.ID, r, result); err != nil {
					return err
				}
				// Only the owner's same-transaction effect acknowledgement can
				// advance this marker. A pruned identity cannot adopt a new ID.
				next.Version.MutationRevision = result.VersionMutationRevision
				return nil
			})
		})
	return err //nolint:wrapcheck // guarded removal preserves its refusal classification
}

func (x Executor) acknowledge(ctx context.Context, run, version int64, r *model.CleanupRun, result artifactsapi.RemovalResult) error {
	if result.Files <= 0 || result.Files != int64(len(result.Items)) {
		return fmt.Errorf("cleanup removal returned inconsistent file effects: %w", kernel.ErrStore)
	}
	var bytes int64
	for _, removed := range result.Items {
		if removed.Size < 0 || removed.Size > math.MaxInt64-bytes {
			return fmt.Errorf("cleanup removal bytes overflow: %w", kernel.ErrStore)
		}
		bytes += removed.Size
	}
	if bytes != result.Bytes {
		return fmt.Errorf("cleanup removal returned inconsistent bytes: %w", kernel.ErrStore)
	}
	for _, removed := range result.Items {
		entry, err := x.Store.Cleanup().Entry(ctx, run, model.CleanupItemEntry, removed.ID)
		if errors.Is(err, kernel.ErrNotFound) {
			entry = model.CleanupEntry{
				RunID:     run,
				EntityID:  removed.ID,
				VersionID: version,
				Revision:  1,
				Kind:      model.CleanupItemEntry,
				State:     model.CleanupDeleted,
				Removed:   &removed,
				Reason:    "companion_removed",
			}
			created, err := x.Store.Cleanup().CreateEntry(ctx, entry)
			if err != nil {
				return fmt.Errorf("record cleanup companion effect: %w", err)
			}
			if !created {
				continue
			}
		} else {
			if err != nil {
				return fmt.Errorf("read cleanup effect acknowledgement: %w", err)
			}
			if entry.State == model.CleanupDeleted {
				continue
			}
			old := entry.Revision
			entry.Revision++
			entry.State, entry.Removed, entry.Reason = model.CleanupDeleted, &removed, "removed"
			if err := x.Store.Cleanup().AdvanceEntry(ctx, old, entry); err != nil {
				return fmt.Errorf("acknowledge cleanup effect: %w", err)
			}
		}
		if r.CleanedItems == math.MaxInt64 || removed.Size > math.MaxInt64-r.CleanedSize {
			return fmt.Errorf("cleanup acknowledged totals overflow: %w", kernel.ErrStore)
		}
		r.CleanedItems++
		r.CleanedSize += removed.Size
	}
	return nil
}

func (x Executor) skipStep(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error) error {
	return x.publish(ctx, e, p, fence, true, func(ctx context.Context, r *model.CleanupRun, current *model.CleanupProgress) error {
		state := model.CleanupPending
		if r.DryRun {
			state = model.CleanupSelected
		}
		rows, next, err := x.Store.Cleanup().
			Entries(ctx, port.CleanupEntryQuery{RunID: e.RunID, VersionID: p.Version.ID, Kind: model.CleanupItemEntry, State: state}, kernel.Page{Limit: executionPageSize})
		if err != nil {
			return fmt.Errorf("list refused cleanup intents: %w", err)
		}
		for _, entry := range rows {
			old := entry.Revision
			entry.Revision++
			entry.State, entry.Reason = model.CleanupSkipped, p.Reason
			if err := x.Store.Cleanup().AdvanceEntry(ctx, old, entry); err != nil {
				return fmt.Errorf("record refused cleanup intent: %w", err)
			}
		}
		if next == "" {
			afterVersion(current)
			x.settleTraversal(r, *current)
		}
		return nil
	})
}

func (x Executor) refuseVersion(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error, cause error) error {
	return x.withCaller(ctx, e.Credential, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(e.Policy.ProjectKey) {
			return kernel.ErrForbidden
		}
		if err := x.Policies.currentProject(ctx, e.Policy); err != nil {
			return err
		}
		return x.publish(ctx, e, p, fence, true, func(ctx context.Context, _ *model.CleanupRun, current *model.CleanupProgress) error {
			if errors.Is(cause, kernel.ErrForbidden) {
				entry, err := x.Store.Cleanup().PendingEntry(ctx, e.RunID, p.Version.ID)
				if err != nil {
					return fmt.Errorf("read denied cleanup intent: %w", err)
				}
				old := entry.Revision
				entry.Revision++
				entry.State, entry.Reason = model.CleanupEntryFailed, "delete_permission_denied"
				if err := x.Store.Cleanup().AdvanceEntry(ctx, old, entry); err != nil {
					return fmt.Errorf("record denied cleanup intent: %w", err)
				}
				current.HadErrors = true
				return x.versionOutcome(ctx, e, p, model.CleanupEntryFailed, "delete_permission_denied")
			} else {
				current.Phase, current.Reason = model.CleanupSkipping, string(ReasonChanged)
				return x.versionOutcome(ctx, e, p, model.CleanupSkipped, string(ReasonChanged))
			}
		})
	})
}

func (x Executor) versionOutcome(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, state model.CleanupEntryState, reason string) error {
	entry, err := x.Store.Cleanup().Entry(ctx, e.RunID, model.CleanupVersionEntry, p.Version.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		_, err = x.Store.Cleanup().CreateEntry(ctx, versionDecision(e.RunID, p.Version, state, reason))
		if err != nil {
			return fmt.Errorf("record absent cleanup version decision: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cleanup version decision: %w", err)
	}
	if entry.State == model.CleanupEntryFailed || entry.State == state && entry.Reason == reason {
		return nil
	}
	old := entry.Revision
	entry.Revision++
	entry.State, entry.Reason = state, reason
	if err := x.Store.Cleanup().AdvanceEntry(ctx, old, entry); err != nil {
		return fmt.Errorf("record cleanup version outcome: %w", err)
	}
	return nil
}

// A repository/package/version may disappear between durable pages. Advance
// past its saved parent cursor rather than retrying an obsolete parent forever.
func (x Executor) skipTraversal(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error) error {
	return x.withCaller(ctx, e.Credential, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(e.Policy.ProjectKey) {
			return kernel.ErrForbidden
		}
		if err := x.Policies.currentProject(ctx, e.Policy); err != nil {
			return err
		}
		return x.publish(ctx, e, p, fence, true, func(ctx context.Context, r *model.CleanupRun, current *model.CleanupProgress) error {
			switch p.Phase {
			case model.CleanupPackages:
				afterRepository(current)
			case model.CleanupVersions:
				afterPackage(current)
			case model.CleanupCandidates:
				current.Phase, current.Reason = model.CleanupSkipping, string(ReasonChanged)
				if err := x.versionOutcome(ctx, e, p, model.CleanupSkipped, string(ReasonChanged)); err != nil {
					return err
				}
			}
			x.settleTraversal(r, *current)
			return nil
		})
	})
}
