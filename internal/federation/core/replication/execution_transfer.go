package replication

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TransferStep performs a bounded direction step. Its observed intent writes
// and terminal transition must join execution proof before the final fence.
type TransferStep func(context.Context, model.ExecutionRun, PushFence) error

// ExecutionStep binds actual push checkpoints to their logical execution.
// The wrapper observes successful store operations inside the driver's own
// source-authorized transaction; it never counts a remote acknowledgement later.
func (p Push) ExecutionStep(ctx context.Context, execution model.ExecutionRun, progress port.ExecutionProgressStore, fence PushFence) error {
	if progress == nil || p.Store == nil || execution.Binding.Direction != model.ReplicationPush {
		return fmt.Errorf("push execution contracts unavailable: %w", kernel.ErrStore)
	}
	ref := model.PushReference{ID: execution.Binding.TransferID, Incarnation: execution.Binding.TransferIncarnation}
	run, err := p.Store.PushProgress().Run(ctx, ref)
	if err != nil {
		return fmt.Errorf("read execution push lifetime: %w", err)
	}
	if run.Binding.TargetKey != execution.Binding.TargetKey || run.Binding.TargetRevision != execution.Binding.TargetRevision ||
		run.Binding.Source != execution.Binding.Repository || run.Binding.Credential != execution.Binding.Credential || run.Binding.Root != execution.Binding.Root {
		return fmt.Errorf("push execution binding changed: %w", kernel.ErrConflict)
	}
	if run.Phase.Terminal() {
		return fmt.Errorf("push completion has no atomic execution projection: %w", kernel.ErrStore)
	}
	p.Store = observedPushStore{PushStore: p.Store, progress: progress, execution: execution.Reference}
	options, err := executionOptions(ctx, execution, progress)
	if err != nil {
		return err
	}
	p.Configurations = executionConfiguration(p.Configurations.WithExecutionOptions(options), execution)
	_, err = p.Step(ctx, ref, fence)
	return err
}

type observedPushStore struct {
	port.PushStore
	progress  port.ExecutionProgressStore
	execution model.ExecutionReference
}

func (s observedPushStore) PushProgress() port.PushProgressStore {
	return observedPushProgress{s.PushStore.PushProgress(), s.progress, s.execution}
}

type observedPushProgress struct {
	port.PushProgressStore
	progress  port.ExecutionProgressStore
	execution model.ExecutionReference
}

func (p observedPushProgress) observe(ctx context.Context, item model.PushItem) error {
	key, err := model.PushItemKey(item.Action, item.Path)
	if err != nil {
		return fmt.Errorf("push execution operation: %w", err)
	}
	fingerprint, err := model.PushIntentFingerprint(item)
	if err != nil {
		return fmt.Errorf("push execution intent: %w", err)
	}
	effect := model.ExecutionEffect{
		Run: p.execution, TransferID: item.Run.ID, TransferIncarnation: item.Run.Incarnation,
		OperationKey: key, Fingerprint: fingerprint, Path: item.Path, Version: item.Version,
		Outcome: model.EffectPending, Updated: item.Updated,
	}
	if item.VerifiedTarget != nil || item.TargetAbsent {
		effect.Paths = []string{item.Path}
	}
	if item.Phase == model.PushItemComplete {
		effect.Outcome, effect.Reason = model.EffectOutcome(item.Outcome), item.Reason
	}
	if err := p.progress.RecordEffect(ctx, effect); err != nil {
		return fmt.Errorf("project actual push evidence: %w", err)
	}
	return nil
}

func (p observedPushProgress) CreateItem(ctx context.Context, input model.PushItem) (model.PushItem, error) {
	item, err := p.PushProgressStore.CreateItem(ctx, input)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("create observed push intent: %w", err)
	}
	if err := p.observe(ctx, item); err != nil {
		return model.PushItem{}, err
	}
	return item, nil
}

func (p observedPushProgress) AdvanceItem(ctx context.Context, expected int64, next model.PushItem) error {
	if err := p.PushProgressStore.AdvanceItem(ctx, expected, next); err != nil {
		return fmt.Errorf("advance observed push intent: %w", err)
	}
	return p.observe(ctx, next)
}

func (p observedPushProgress) Advance(ctx context.Context, expected int64, next model.PushRun) error {
	if err := p.PushProgressStore.Advance(ctx, expected, next); err != nil {
		return fmt.Errorf("advance observed push traversal: %w", err)
	}
	if next.Phase.Terminal() {
		return finishTransferExecution(ctx, p.progress, p.execution, next.Phase == model.PushComplete, next.Updated)
	}
	return nil
}

// ExecutionStep binds artifact-owner pull receipts and exact removal paths to
// dispatch in the same cache/progress transaction, before queue lease fencing.
func (p Pull) ExecutionStep(ctx context.Context, execution model.ExecutionRun, progress port.ExecutionProgressStore, fence PushFence) error {
	if progress == nil || p.Store == nil || execution.Binding.Direction != model.ReplicationPull {
		return fmt.Errorf("pull execution contracts unavailable: %w", kernel.ErrStore)
	}
	ref := model.PullReference{ID: execution.Binding.TransferID, Incarnation: execution.Binding.TransferIncarnation}
	run, err := p.Store.PullProgress().Run(ctx, ref)
	if err != nil {
		return fmt.Errorf("read execution pull lifetime: %w", err)
	}
	if run.Binding.TargetKey != execution.Binding.TargetKey || run.Binding.TargetRevision != execution.Binding.TargetRevision ||
		run.Binding.Source.Repository != execution.Binding.Repository || run.Binding.Credential != execution.Binding.Credential || run.Binding.Root != execution.Binding.Root {
		return fmt.Errorf("pull execution binding changed: %w", kernel.ErrConflict)
	}
	if run.Phase.Terminal() {
		return fmt.Errorf("pull completion has no atomic execution projection: %w", kernel.ErrStore)
	}
	p.Store = observedPullStore{PullStore: p.Store, progress: progress, execution: execution.Reference}
	options, err := executionOptions(ctx, execution, progress)
	if err != nil {
		return err
	}
	p.Configurations = executionConfiguration(p.Configurations.WithExecutionOptions(options), execution)
	_, err = p.Step(ctx, ref, fence)
	return err
}

type observedPullStore struct {
	port.PullStore
	progress  port.ExecutionProgressStore
	execution model.ExecutionReference
}

func (s observedPullStore) PullProgress() port.PullProgressStore {
	return observedPullProgress{s.PullStore.PullProgress(), s.progress, s.execution}
}

type observedPullProgress struct {
	port.PullProgressStore
	progress  port.ExecutionProgressStore
	execution model.ExecutionReference
}

func (p observedPullProgress) observe(ctx context.Context, item model.PullItem) error {
	key, err := model.PullItemKey(item.Action, item.Path)
	if err != nil {
		return fmt.Errorf("pull execution operation: %w", err)
	}
	fingerprint, err := model.PullIntentFingerprint(item)
	if err != nil {
		return fmt.Errorf("pull execution intent: %w", err)
	}
	effect := model.ExecutionEffect{
		Run: p.execution, TransferID: item.Run.ID, TransferIncarnation: item.Run.Incarnation,
		OperationKey: key, Fingerprint: fingerprint, Path: item.Path, Version: item.Version,
		Outcome: model.EffectPending, Updated: item.Updated,
	}
	if item.Phase == model.PullItemComplete {
		effect.Outcome, effect.Reason = model.EffectOutcome(item.Outcome), item.Reason
		if item.Cache != nil {
			effect.Paths = []string{item.Path}
		} else {
			effect.Paths = item.Removed
		}
	}
	if err := p.progress.RecordEffect(ctx, effect); err != nil {
		return fmt.Errorf("project actual pull evidence: %w", err)
	}
	return nil
}

func (p observedPullProgress) CreateItem(ctx context.Context, input model.PullItem) (model.PullItem, error) {
	item, err := p.PullProgressStore.CreateItem(ctx, input)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("create observed pull intent: %w", err)
	}
	if err := p.observe(ctx, item); err != nil {
		return model.PullItem{}, err
	}
	return item, nil
}

func (p observedPullProgress) AdvanceItem(ctx context.Context, expected int64, next model.PullItem) error {
	if err := p.PullProgressStore.AdvanceItem(ctx, expected, next); err != nil {
		return fmt.Errorf("advance observed pull intent: %w", err)
	}
	return p.observe(ctx, next)
}

func (p observedPullProgress) Advance(ctx context.Context, expected int64, next model.PullRun) error {
	if err := p.PullProgressStore.Advance(ctx, expected, next); err != nil {
		return fmt.Errorf("advance observed pull traversal: %w", err)
	}
	if next.Phase.Terminal() {
		return finishTransferExecution(ctx, p.progress, p.execution, next.Phase == model.PullComplete, next.Updated)
	}
	return nil
}

func finishTransferExecution(ctx context.Context, progress port.ExecutionProgressStore, ref model.ExecutionReference, complete bool, at time.Time) error {
	current, err := progress.Run(ctx, ref)
	if err != nil {
		return fmt.Errorf("read completing execution: %w", err)
	}
	next := current
	next.Version++
	if at.After(next.Updated) {
		next.Updated = at
	}
	next.Completed = next.Updated
	next.Phase = model.ExecutionAborted
	next.Failure = model.ExecutionTransferFailed
	if complete {
		next.Phase = model.ExecutionComplete
		next.Failure = ""
	}
	if err := progress.Advance(ctx, current.Version, next); err != nil {
		return fmt.Errorf("project terminal transfer: %w", err)
	}
	return nil
}

func executionConfiguration(service Service, run model.ExecutionRun) Service {
	guard := service.Guard
	service.Guard = func(ctx context.Context) error {
		if guard == nil || service.Authorize == nil {
			return fmt.Errorf("replication administration guard unavailable: %w", kernel.ErrStore)
		}
		if err := guard(ctx); err != nil {
			return fmt.Errorf("guard execution administration: %w", err)
		}
		policy, err := service.Authorize(ctx)
		if err != nil {
			return fmt.Errorf("current execution administration: %w", err)
		}
		if policy == nil || !policy.Any() {
			return kernel.ErrForbidden
		}
		repository, err := service.repository(ctx, run.Binding.Repository.Key, policy)
		if err != nil {
			return err
		}
		if direction, ok := model.DirectionFor(repository.Class); !ok || direction != run.Binding.Direction {
			return kernel.ErrConflict
		}
		return nil
	}
	return service
}
