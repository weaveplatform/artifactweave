package cleanup

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// beginFailure records an exhausted execution attempt after the failed effect
// rolled back. It never acknowledges uncommitted deletion or stores raw errors.
func (x Executor) beginFailure(ctx context.Context, id int64, fence func(context.Context) error) error {
	err := x.Store.Tx(ctx, func(ctx context.Context) error {
		if err := x.Policies.Guard(ctx); err != nil {
			return err
		}
		e, err := x.Store.Cleanup().Execution(ctx, id)
		if err != nil {
			return fmt.Errorf("read failing cleanup execution: %w", err)
		}
		p, err := x.Store.Cleanup().Progress(ctx, id)
		if err != nil {
			return fmt.Errorf("read failing cleanup continuation: %w", err)
		}
		r, err := x.Store.Cleanup().Run(ctx, id)
		if err != nil {
			return fmt.Errorf("read failing cleanup run: %w", err)
		}
		if r.Status.Terminal() {
			return nil
		}
		if r.Status == api.RunStopping || p.Phase == model.CleanupFailing {
			return work.ErrUnsettled
		}
		if x.expired(e, r) {
			_, err := x.finish(ctx, e, p, fence, api.RunStopped, "duration_limit")
			return err
		}
		return x.publish(ctx, e, p, fence, false, func(ctx context.Context, _ *model.CleanupRun, current *model.CleanupProgress) error {
			if p.Phase == model.CleanupRemoving {
				entry, err := x.Store.Cleanup().PendingEntry(ctx, id, p.Version.ID)
				if err != nil && !errors.Is(err, kernel.ErrNotFound) {
					return fmt.Errorf("read failed cleanup intent: %w", err)
				}
				if err == nil {
					old := entry.Revision
					entry.Revision++
					entry.State, entry.Reason = model.CleanupEntryFailed, "work_retries_exhausted"
					if err := x.Store.Cleanup().AdvanceEntry(ctx, old, entry); err != nil {
						return fmt.Errorf("record failed cleanup intent: %w", err)
					}
				}
				if err := x.versionOutcome(ctx, e, p, model.CleanupEntryFailed, "work_retries_exhausted"); err != nil {
					return err
				}
			}
			current.Phase, current.Reason, current.HadErrors = model.CleanupFailing, "work_retries_exhausted", true
			return nil
		})
	})
	if err != nil {
		return fmt.Errorf("record cleanup retry exhaustion: %w", err)
	}
	return nil
}

// failureStep retires at most one page of unattempted intents before FAILED.
// Previously acknowledged effects remain intact and are never counted again.
func (x Executor) failureStep(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error) error {
	return x.publish(ctx, e, p, fence, false, func(ctx context.Context, r *model.CleanupRun, current *model.CleanupProgress) error {
		if r.Status == api.RunStopping {
			r.Status, r.Ended = api.RunStopped, x.Policies.now()
			current.Phase, current.Reason = model.CleanupFinished, "stop_requested"
			return nil
		}
		for _, state := range []model.CleanupEntryState{model.CleanupPending, model.CleanupSelected} {
			rows, _, err := x.Store.Cleanup().
				Entries(ctx, port.CleanupEntryQuery{RunID: e.RunID, Kind: model.CleanupItemEntry, State: state}, kernel.Page{Limit: executionPageSize})
			if err != nil {
				return fmt.Errorf("read unattempted cleanup intents: %w", err)
			}
			if len(rows) == 0 {
				continue
			}
			for _, entry := range rows {
				old := entry.Revision
				entry.Revision++
				entry.State, entry.Reason = model.CleanupSkipped, "run_failed"
				if err := x.Store.Cleanup().AdvanceEntry(ctx, old, entry); err != nil {
					return fmt.Errorf("record unattempted cleanup intent: %w", err)
				}
			}
			return nil
		}
		r.Status, r.Ended = api.RunFailed, x.Policies.now()
		current.Phase = model.CleanupFinished
		return nil
	})
}

// Settling an already failed attempt changes private outcomes only. Like
// finish, it needs current queue ownership but no enduring caller capability.
func (x Executor) settleFailure(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error) (bool, error) {
	err := x.Store.Tx(ctx, func(ctx context.Context) error {
		if err := x.Policies.Guard(ctx); err != nil {
			return err
		}
		return x.failureStep(ctx, e, p, fence)
	})
	if err != nil {
		return false, fmt.Errorf("settle failed cleanup outcomes: %w", err)
	}
	r, err := x.Store.Cleanup().Run(ctx, e.RunID)
	if err != nil {
		return false, fmt.Errorf("read failed cleanup settlement: %w", err)
	}
	return r.Status.Terminal(), nil
}
