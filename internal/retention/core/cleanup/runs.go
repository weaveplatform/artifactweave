package cleanup

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// WorkKind uses the logical integer run ID as its key. Two runs of one policy
// must never coalesce, and queue payloads contain no policy or credential copy.
const WorkKind = "cleanup:run"

var activeStatuses = []api.RunStatus{api.RunInProgress, api.RunInProgressWithErrors, api.RunStopping}

// PublicRunID explicitly converts the existing integer storage identity to the
// authored HTTP integer. Reject overflow before an admission transaction commits.
func PublicRunID(id int64) (int, error) {
	if id <= 0 {
		return 0, fmt.Errorf("invalid cleanup run identity: %w", kernel.ErrInvalid)
	}
	public, err := strconv.Atoi(strconv.FormatInt(id, 10))
	if err != nil {
		return 0, fmt.Errorf("cleanup run identity exceeds the public integer: %w", kernel.ErrStore)
	}
	return public, nil
}

// ManualRun records the current caller, frozen policy, stable trigger and work
// in one transaction. The worker reauthenticates the reference before selection
// and again at destructive publication; this snapshot confers no permission.
func (s Service) ManualRun(ctx context.Context, key string, dryRun bool) (model.CleanupRun, error) {
	var out model.CleanupRun
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		p, err := s.policy(ctx, key, authority)
		if err != nil {
			return err
		}
		if !p.Enabled || p.Revision == "" {
			return fmt.Errorf("cleanup policy is disabled: %w", kernel.ErrInvalid)
		}
		out, err = s.admitRun(ctx, p, authority, dryRun, true, "")
		return err
	})
	if err != nil {
		return model.CleanupRun{}, err
	}
	return out, nil
}

// ScheduledRun deduplicates a logical scheduler occurrence under the current
// policy/project guard. A legacy name-only editor is never impersonated: such
// policies need enablement by a verified credential before scheduled execution.
func (s Service) ScheduledRun(ctx context.Context, trigger schedule.Trigger) (model.CleanupRun, error) {
	if s.Store == nil || s.Guard == nil || s.Authorize == nil {
		return model.CleanupRun{}, fmt.Errorf("cleanup scheduling is unavailable: %w", kernel.ErrStore)
	}
	if trigger.Reference.Kind != ScheduleKind || trigger.ID == "" || trigger.Revision == "" || trigger.Fire.IsZero() || trigger.Fence == nil {
		return model.CleanupRun{}, fmt.Errorf("invalid cleanup schedule occurrence: %w", kernel.ErrInvalid)
	}
	var out model.CleanupRun
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("guard scheduled cleanup: %w", err)
		}
		p, err := s.Store.Cleanup().Policy(ctx, trigger.Reference.Key)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read scheduled cleanup policy: %w", err)
		}
		if !p.Enabled || p.Revision != trigger.Revision || p.ItemType != model.CleanupPackage {
			return nil
		}
		if err := model.ValidateCleanupCredential(p.ExecutionCredential); err != nil {
			return err //nolint:wrapcheck // a legacy actor name is not a verified grant
		}
		ctx = kernel.WithPrincipal(ctx, p.ExecutionCredential.Principal())
		authority, err := s.Authorize(ctx)
		if err != nil {
			return fmt.Errorf("current scheduled cleanup caller: %w", err)
		}
		if authority == nil || !authority.Any() || !authority.Allows(p.ProjectKey) {
			return kernel.ErrForbidden
		}
		previous, err := s.Store.Cleanup().ExecutionForOccurrence(ctx, trigger.ID)
		if err == nil {
			out, err = s.Store.Cleanup().Run(ctx, previous.RunID)
		} else if errors.Is(err, kernel.ErrNotFound) {
			out, err = s.admitRun(ctx, p, authority, false, false, trigger.ID)
		}
		if err != nil {
			return fmt.Errorf("admit cleanup occurrence: %w", err)
		}
		// All policy/run/queue writes precede the scheduler lease fence. An old
		// worker must not publish an occurrence after its leased generation ends.
		return trigger.Fence(ctx) //nolint:wrapcheck // the scheduler names the lost lease
	})
	if err != nil {
		return model.CleanupRun{}, fmt.Errorf("scheduled cleanup admission: %w", err)
	}
	return out, nil
}

func (s Service) admitRun(ctx context.Context, p model.CleanupPolicy, authority Policy, dryRun, manual bool, occurrence string) (model.CleanupRun, error) {
	if s.Work == nil {
		return model.CleanupRun{}, fmt.Errorf("cleanup work queue is unavailable: %w", kernel.ErrStore)
	}
	credential := authority.Credential()
	if err := model.ValidateCleanupCredential(credential); err != nil {
		return model.CleanupRun{}, err //nolint:wrapcheck // the model names the missing credential
	}
	if credential.Name != authority.Name() {
		return model.CleanupRun{}, kernel.ErrBadCredentials
	}
	active, _, err := s.Store.Cleanup().Runs(ctx, port.RunFilter{PolicyKey: p.Key, ItemType: model.CleanupPackage, Statuses: activeStatuses}, kernel.Page{Limit: 1})
	if err != nil {
		return model.CleanupRun{}, fmt.Errorf("read active cleanup runs: %w", err)
	}
	if len(active) != 0 {
		return model.CleanupRun{}, fmt.Errorf("cleanup policy already has an active run: %w", kernel.ErrInvalid)
	}
	r, err := s.Store.Cleanup().CreateRun(ctx, model.CleanupRun{
		PolicyKey: p.Key, ItemType: model.CleanupPackage, Status: api.RunInProgress,
		DryRun: dryRun, Manual: manual, TriggeredBy: authority.Name(),
	})
	if err != nil {
		return model.CleanupRun{}, fmt.Errorf("create cleanup run: %w", err)
	}
	if _, err := PublicRunID(r.ID); err != nil {
		return model.CleanupRun{}, err
	}
	r.TriggerID = r.ID
	if err := s.Store.Cleanup().UpdateRun(ctx, r); err != nil {
		return model.CleanupRun{}, fmt.Errorf("assign cleanup trigger identity: %w", err)
	}
	p.ExecutionCredential = kernel.CredentialReference{}
	execution := model.CleanupExecution{RunID: r.ID, Occurrence: occurrence, Policy: kernel.Clone(p), Credential: credential, At: s.now()}
	if err := s.Store.Cleanup().CreateExecution(ctx, execution); err != nil {
		return model.CleanupRun{}, fmt.Errorf("record cleanup execution snapshot: %w", err)
	}
	if err := s.Work.Enqueue(ctx, WorkKind, strconv.FormatInt(r.ID, 10), nil, s.now()); err != nil {
		return model.CleanupRun{}, fmt.Errorf("enqueue cleanup run: %w", err)
	}
	return r, nil
}

// Stop fences future effects without claiming they have already stopped. The
// executor alone publishes STOPPED, after crossing its final destructive fence.
// Historical scope comes from the private run snapshot even after policy deletion.
func (s Service) Stop(ctx context.Context, id int64) error {
	return s.run(ctx, func(ctx context.Context, authority Policy) error {
		e, err := s.Store.Cleanup().Execution(ctx, id)
		if err != nil {
			return fmt.Errorf("read cleanup stop scope: %w", err)
		}
		if !authority.Allows(e.Policy.ProjectKey) {
			return kernel.ErrForbidden
		}
		return s.stopRun(ctx, id)
	})
}

func (s Service) stopRun(ctx context.Context, id int64) error {
	r, err := s.Store.Cleanup().Run(ctx, id)
	if err != nil {
		return fmt.Errorf("read cleanup stop state: %w", err)
	}
	if r.Status.Terminal() || r.Status == api.RunStopping {
		return nil
	}
	r.Status = api.RunStopping
	if err := s.Store.Cleanup().UpdateRun(ctx, r); err != nil {
		return fmt.Errorf("request cleanup stop: %w", err)
	}
	return nil
}

// StopAll is the platform administrator's force-stop operation. It leaves
// terminal history unchanged and reports only a stop request, never completion.
func (s Service) StopAll(ctx context.Context) error {
	return s.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows("") {
			return kernel.ErrForbidden
		}
		return s.stopRuns(ctx, nil)
	})
}

func (s Service) stopRuns(ctx context.Context, project *string) error {
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		runs, next, err := s.Store.Cleanup().Runs(ctx, port.RunFilter{ItemType: model.CleanupPackage, Statuses: activeStatuses}, page)
		if err != nil {
			return fmt.Errorf("list cleanup stop requests: %w", err)
		}
		for _, r := range runs {
			if project != nil {
				e, err := s.Store.Cleanup().Execution(ctx, r.ID)
				if errors.Is(err, kernel.ErrNotFound) {
					continue // Legacy placeholder runs have no executing capability.
				}
				if err != nil {
					return fmt.Errorf("read cleanup stop project: %w", err)
				}
				if e.Policy.ProjectKey != *project {
					continue
				}
			}
			if err := s.stopRun(ctx, r.ID); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}
