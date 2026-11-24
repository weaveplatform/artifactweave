package federation

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type executionStore interface {
	Store
	port.PushStore
	port.PullStore
	replication.CoordinatorStore
}

func (m *Module) executionDrivers() (executionStore, replication.Push, replication.Pull, error) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	store, ok := m.deps.Store.(executionStore)
	if !ok || m.closed || m.push == nil || m.pull == nil {
		return nil, replication.Push{}, replication.Pull{}, fmt.Errorf("single-replication execution is not open: %w", kernel.ErrStore)
	}
	return store, *m.push, *m.pull, nil
}

// Coordinator binds manual and Quartz admission to the real direction drivers.
// Construction makes no peer request and does not enqueue or lease any work.
func (m *Module) Coordinator() (replication.Coordinator, error) {
	store, push, pull, err := m.executionDrivers()
	if err != nil {
		return replication.Coordinator{}, err
	}
	return replication.Coordinator{
		Store: store, Configurations: m.Configurations(), Restore: m.restoreTransferCaller,
		Drivers: map[model.ReplicationDirection]replication.TransferStart{
			model.ReplicationPush: func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				driver := push
				driver.Configurations = driver.Configurations.WithExecutionOptions(request.Options)
				run, err := driver.Start(
					ctx,
					replication.PushRequest{ID: request.ID, TargetKey: request.TargetKey, TargetRevision: request.TargetRevision, Root: request.Root},
					nil,
				)
				if err != nil {
					return replication.TransferAdmission{}, fmt.Errorf("admit execution push: %w", err)
				}
				return replication.TransferAdmission{ID: run.Reference.ID, Incarnation: run.Reference.Incarnation, Repository: run.Binding.Source}, nil
			},
			model.ReplicationPull: func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				driver := pull
				driver.Configurations = driver.Configurations.WithExecutionOptions(request.Options)
				run, err := driver.Start(
					ctx,
					replication.PullRequest{ID: request.ID, TargetKey: request.TargetKey, TargetRevision: request.TargetRevision, Root: request.Root},
					nil,
				)
				if err != nil {
					return replication.TransferAdmission{}, fmt.Errorf("admit execution pull: %w", err)
				}
				return replication.TransferAdmission{ID: run.Reference.ID, Incarnation: run.Reference.Incarnation, Repository: run.Binding.Source.Repository}, nil
			},
		},
	}, nil
}

// Worker projects actual direction checkpoints under current work ownership.
func (m *Module) Worker() (replication.Worker, error) {
	store, push, pull, err := m.executionDrivers()
	if err != nil {
		return replication.Worker{}, err
	}
	return replication.Worker{
		Store: store, Configurations: m.Configurations(), Restore: m.restoreTransferCaller,
		Drivers: map[model.ReplicationDirection]replication.TransferStep{
			model.ReplicationPush: func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
				return push.ExecutionStep(ctx, run, store.Executions(), fence)
			},
			model.ReplicationPull: func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
				return pull.ExecutionStep(ctx, run, store.Executions(), fence)
			},
		},
	}, nil
}

// RegisterWork binds the existing queue before its application loop starts:
// replication execution, the periodic pruning of expired pairing nonces,
// whose item it queues once, stream delivery once OpenStreams has run,
// and configSync once OpenConfigSync has run.
func (m *Module) RegisterWork(ctx context.Context, queue *work.Queue) error {
	if queue == nil {
		return fmt.Errorf("replication work queue unavailable: %w", kernel.ErrStore)
	}
	worker, err := m.Worker()
	if err != nil {
		return err
	}
	if err := queue.Handle(replication.ExecutionWorkKind, worker.Execute); err != nil {
		return fmt.Errorf("register replication execution: %w", err)
	}
	if err := m.registerPairingWork(ctx, queue); err != nil {
		return err
	}
	if err := m.registerStreamWork(ctx, queue); err != nil {
		return err
	}
	return m.registerConfigSyncWork(queue)
}

// ScheduleHandlers resolves the initialized coordinator when an occurrence
// fires. Registration itself can precede source/HTTP-pool initialization.
func (m *Module) ScheduleHandlers() map[string]schedule.Handler {
	return map[string]schedule.Handler{replication.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
		coordinator, err := m.Coordinator()
		if err != nil {
			return err
		}
		if _, err := coordinator.Scheduled(ctx, trigger); err != nil {
			return fmt.Errorf("admit scheduled single replication: %w", err)
		}
		return nil
	}}
}
