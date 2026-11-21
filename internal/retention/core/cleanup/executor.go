package cleanup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// Executor performs one bounded metadata/deletion step at a time. Removal owns
// normal trash/index/usage/event effects; its record callback joins their exact
// transaction with progress and the final current work-lease fence.
type Executor struct {
	Store    port.CleanupExecutionStore
	Policies Service
	Removal  artifactsapi.ItemRemoval
}

type executionRefusal struct{ cause error }

func (e *executionRefusal) Error() string { return "cleanup execution publication refused" }
func (e *executionRefusal) Unwrap() error { return e.cause }

type stopRequired struct{ reason string }

func (e *stopRequired) Error() string { return "cleanup execution must stop: " + e.reason }

type authorityRefusal struct{ cause error }

func (e *authorityRefusal) Error() string { return "cleanup execution authority ended" }
func (e *authorityRefusal) Unwrap() error { return e.cause }

// Execute retains queue cancellation/backoff semantics. A shutdown returns the
// cancellation to work.Queue rather than falsely marking this run completed.
func (x Executor) Execute(ctx context.Context, item work.WorkItem) error {
	id, err := strconv.ParseInt(item.Key, 10, 64)
	if err != nil || id <= 0 || item.Kind != WorkKind || strconv.FormatInt(id, 10) != item.Key {
		return fmt.Errorf("invalid cleanup work reference: %w", work.ErrPermanent)
	}
	fence := func(ctx context.Context) error {
		until, err := x.Store.WorkQueue().GuardLease(ctx, item.Kind, item.Key, item.Generation)
		if err != nil {
			return fmt.Errorf("guard cleanup work lease: %w", err)
		}
		if !until.After(x.Policies.now()) {
			return fmt.Errorf("cleanup work lease expired: %w", kernel.ErrConflict)
		}
		return ctx.Err()
	}
	for {
		done, err := x.Step(ctx, id, fence)
		if err != nil {
			if !item.FinalAttempt || ctx.Err() != nil {
				return err
			}
			if failure := x.beginFailure(ctx, id, fence); failure != nil {
				return fmt.Errorf("cleanup outcome not recorded: %w", errors.Join(work.ErrUnsettled, err, failure))
			}
			continue
		}
		if done {
			return nil
		}
	}
}

// Step transfers no bytes and owns its transaction boundaries, releasing each
// discovery page before reading the next. A caller supplies an exclusive fence;
// queue workers bind it to GuardLease. Publication runs that fence after domain
// finalizers, so no later domain lock follows the lease.
func (x Executor) Step(ctx context.Context, id int64, fence func(context.Context) error) (bool, error) {
	if x.Store == nil || x.Policies.Guard == nil || x.Policies.Restore == nil || x.Removal == nil || fence == nil {
		return false, fmt.Errorf("cleanup executor unavailable: %w", kernel.ErrStore)
	}
	if x.Store.InTx(ctx) {
		return false, fmt.Errorf("cleanup step needs an external context: %w", kernel.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("cleanup execution cancelled: %w", err)
	}
	e, err := x.Store.Cleanup().Execution(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read cleanup execution: %w", err)
	}
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("validate cleanup execution: %w", err)
	}
	p, err := x.Store.Cleanup().Progress(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read cleanup continuation: %w", err)
	}
	r, err := x.Store.Cleanup().Run(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read cleanup run: %w", err)
	}
	if r.Status.Terminal() {
		return true, nil
	}
	if p.Phase == model.CleanupFailing {
		return x.settleFailure(ctx, e, p, fence)
	}
	e.Policy.ProjectIncarnation = e.ProjectIncarnation
	selection, err := x.Policies.selection(SelectionSnapshot{Policy: e.Policy, At: e.At})
	if err != nil {
		return false, err
	}
	authorized := false
	if discoveryPhase(p.Phase) && !r.Started.IsZero() {
		authorized, err = x.discover(ctx, e, p, selection, fence)
	} else {
		err = x.withCaller(ctx, e.Credential, func(ctx context.Context, authority Policy) error {
			authorized = true
			return x.authorizedStep(ctx, e, p, selection, fence, authority)
		})
	}
	if err != nil {
		return x.failedStep(ctx, e, p, fence, authorized, err)
	}
	fresh, err := x.Store.Cleanup().Run(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read cleanup step result: %w", err)
	}
	return fresh.Status.Terminal(), nil
}

// withCaller owns restoration and current authority in the same transaction,
// including a refusal recorded after an earlier artifact mutation rolled back.
func (x Executor) withCaller(ctx context.Context, reference kernel.CredentialReference, fn func(context.Context, Policy) error) error {
	err := x.Store.Tx(ctx, func(ctx context.Context) error {
		bound, err := x.Policies.Restore(ctx, reference)
		if err != nil {
			return fmt.Errorf("restore cleanup execution caller: %w", err)
		}
		if bound == nil || !x.Store.InTx(bound) {
			return fmt.Errorf("cleanup restoration lost its transaction: %w", kernel.ErrStore)
		}
		return x.Policies.run(bound, fn)
	})
	if err != nil {
		return fmt.Errorf("cleanup execution authority: %w", err)
	}
	return nil
}

func (x Executor) authorizedStep(
	ctx context.Context,
	e model.CleanupExecution,
	p model.CleanupProgress,
	selection *Selection,
	fence func(context.Context) error,
	authority Policy,
) error {
	if !authority.Allows(e.Policy.ProjectKey) {
		return &authorityRefusal{kernel.ErrForbidden}
	}
	fresh, err := x.currentRun(ctx, e)
	if err != nil {
		return err
	}
	if fresh.Status.Terminal() {
		return nil
	}
	if fresh.Started.IsZero() {
		return x.publish(ctx, e, p, fence, true, func(_ context.Context, r *model.CleanupRun, _ *model.CleanupProgress) error {
			r.Started = x.Policies.now()
			return nil
		})
	}
	if p.Phase == model.CleanupRemoving {
		return x.removeStep(ctx, e, p, selection, fence)
	}
	if p.Phase == model.CleanupSkipping {
		return x.skipStep(ctx, e, p, fence)
	}
	return fmt.Errorf("cleanup discovery needs an external page context: %w", kernel.ErrStore)
}

func (x Executor) currentRun(ctx context.Context, e model.CleanupExecution) (model.CleanupRun, error) {
	if err := x.Policies.currentProject(ctx, e.Policy); err != nil {
		return model.CleanupRun{}, &authorityRefusal{err}
	}
	current, err := x.Policies.Store.Cleanup().Policy(ctx, e.Policy.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.CleanupRun{}, &stopRequired{"policy_deleted"}
	}
	if err != nil {
		return model.CleanupRun{}, fmt.Errorf("read current cleanup policy: %w", err)
	}
	if current.ProjectKey != e.Policy.ProjectKey || current.ItemType != model.CleanupPackage {
		return model.CleanupRun{}, &authorityRefusal{kernel.ErrForbidden}
	}
	fresh, err := x.Store.Cleanup().Run(ctx, e.RunID)
	if err != nil {
		return model.CleanupRun{}, fmt.Errorf("read current cleanup run: %w", err)
	}
	if fresh.Status.Terminal() {
		return fresh, nil
	}
	if fresh.Status == api.RunStopping {
		return model.CleanupRun{}, &stopRequired{"stop_requested"}
	}
	if x.expired(e, fresh) {
		return model.CleanupRun{}, &stopRequired{"duration_limit"}
	}
	return fresh, nil
}

func (x Executor) failedStep(ctx context.Context, e model.CleanupExecution, p model.CleanupProgress, fence func(context.Context) error, authorized bool, err error) (bool, error) {
	var stop *stopRequired
	if errors.As(err, &stop) {
		return x.finish(ctx, e, p, fence, api.RunStopped, stop.reason)
	}
	var refusal *executionRefusal
	if errors.As(err, &refusal) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}
	var ended *authorityRefusal
	if errors.As(err, &ended) || !authorized && errors.Is(err, kernel.ErrForbidden) || errors.Is(err, kernel.ErrBadCredentials) || errors.Is(err, kernel.ErrUnauthenticated) ||
		errors.Is(err, kernel.ErrPasswordExpired) {
		return x.finish(ctx, e, p, fence, api.RunFailed, "authority_revoked")
	}
	if p.Phase == model.CleanupRemoving && (errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrForbidden)) {
		return false, x.refuseVersion(ctx, e, p, fence, err)
	}
	var unavailable *repositoryUnavailable
	if discoveryPhase(p.Phase) && errors.Is(err, kernel.ErrForbidden) && !errors.As(err, &unavailable) {
		return x.finish(ctx, e, p, fence, api.RunFailed, "authority_revoked")
	}
	if (p.Phase == model.CleanupPackages || p.Phase == model.CleanupVersions || p.Phase == model.CleanupCandidates) &&
		(errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) || errors.As(err, &unavailable)) {
		return false, x.skipTraversal(ctx, e, p, fence)
	}
	return false, err
}

func (x Executor) expired(e model.CleanupExecution, r model.CleanupRun) bool {
	return e.Policy.DurationMinutes > 0 && !r.Started.IsZero() && !x.Policies.now().Before(r.Started.Add(time.Duration(e.Policy.DurationMinutes)*time.Minute))
}

func (x Executor) publication(ctx context.Context, e model.CleanupExecution, r model.CleanupRun, fence func(context.Context) error, checkDuration bool) error {
	if err := tx.CheckPublication(ctx); err != nil {
		return fmt.Errorf("settle cleanup publication: %w", err)
	}
	principal, _ := kernel.FromContext(ctx)
	err := tx.BeforeCommit(ctx, func(ctx context.Context) error {
		if err := fence(ctx); err != nil {
			return &executionRefusal{err}
		}
		if checkDuration && x.expired(e, r) {
			return &stopRequired{"duration_limit"}
		}
		if checkDuration && !principal.TokenExpires.IsZero() && !x.Policies.now().Before(principal.TokenExpires) {
			return &authorityRefusal{kernel.ErrBadCredentials}
		}
		return ctx.Err()
	})
	if err != nil {
		return fmt.Errorf("register cleanup publication fence: %w", err)
	}
	return nil
}

func (x Executor) publish(
	ctx context.Context,
	e model.CleanupExecution,
	expected model.CleanupProgress,
	fence func(context.Context) error,
	checkDuration bool,
	effect func(context.Context, *model.CleanupRun, *model.CleanupProgress) error,
) error {
	p, err := x.Store.Cleanup().Progress(ctx, e.RunID)
	if err != nil {
		return fmt.Errorf("lock cleanup continuation: %w", err)
	}
	if p.Revision != expected.Revision {
		return &executionRefusal{kernel.ErrConflict}
	}
	r, err := x.Store.Cleanup().Run(ctx, e.RunID)
	if err != nil {
		return fmt.Errorf("lock cleanup run: %w", err)
	}
	if checkDuration && (r.Status.Terminal() || r.Status == api.RunStopping) {
		return &stopRequired{"stop_requested"}
	}
	if checkDuration && x.expired(e, r) {
		return &stopRequired{"duration_limit"}
	}
	if err := effect(ctx, &r, &p); err != nil {
		return err
	}
	if p.HadErrors && r.Status == api.RunInProgress {
		r.Status = api.RunInProgressWithErrors
	}
	p.Revision++
	if err := x.Store.Cleanup().AdvanceProgress(ctx, expected.Revision, p); err != nil {
		return &executionRefusal{err}
	}
	if err := x.Store.Cleanup().UpdateRun(ctx, r); err != nil {
		return fmt.Errorf("publish cleanup run: %w", err)
	}
	return x.publication(ctx, e, r, fence, checkDuration)
}

func (x Executor) finish(
	ctx context.Context,
	e model.CleanupExecution,
	p model.CleanupProgress,
	fence func(context.Context) error,
	status api.RunStatus,
	reason string,
) (bool, error) {
	err := x.Store.Tx(ctx, func(ctx context.Context) error {
		if err := x.Policies.Guard(ctx); err != nil {
			return err
		}
		return x.publish(ctx, e, p, fence, false, func(_ context.Context, r *model.CleanupRun, p *model.CleanupProgress) error {
			if r.Status.Terminal() {
				return &executionRefusal{kernel.ErrConflict}
			}
			if r.Status == api.RunStopping {
				status = api.RunStopped
				reason = "stop_requested"
			}
			r.Status, r.Ended = status, x.Policies.now()
			p.Phase, p.Reason = model.CleanupFinished, reason
			return nil
		})
	})
	if err != nil {
		return false, fmt.Errorf("finish cleanup execution: %w", err)
	}
	return true, nil
}
