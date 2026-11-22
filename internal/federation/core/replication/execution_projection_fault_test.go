package replication_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type executionPushFaultStore struct {
	port.PushStore
	progress port.PushProgressStore
}

func (s executionPushFaultStore) PushProgress() port.PushProgressStore { return s.progress }

type executionPushFaultProgress struct {
	port.PushProgressStore
	stage string
}

func (p executionPushFaultProgress) CreateItem(ctx context.Context, item model.PushItem) (model.PushItem, error) {
	if p.stage == "intent" {
		return model.PushItem{}, kernel.ErrStore
	}
	return p.PushProgressStore.CreateItem(ctx, item)
}

func (p executionPushFaultProgress) Advance(ctx context.Context, version int64, run model.PushRun) error {
	if p.stage == "cursor" {
		return kernel.ErrStore
	}
	return p.PushProgressStore.Advance(ctx, version, run)
}

func (p executionPushFaultProgress) AdvanceItem(ctx context.Context, version int64, item model.PushItem) error {
	if p.stage == "receipt" && item.Phase == model.PushItemComplete {
		return kernel.ErrStore
	}
	return p.PushProgressStore.AdvanceItem(ctx, version, item)
}

func (w *executionWorld) projectWith(progress port.ExecutionProgressStore) {
	direction := w.run.Binding.Direction
	w.worker.Drivers[direction] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
		if direction == model.ReplicationPush {
			return w.push.push.ExecutionStep(ctx, run, progress, fence)
		}
		return w.pull.driver.ExecutionStep(ctx, run, progress, fence)
	}
}

func TestUnitReplicationObservedDirectionWritesRollBackTheirExecutionEvidence(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		for _, stage := range []string{"intent", "cursor", "receipt", "effect"} {
			t.Run(string(direction)+"/"+stage, func(t *testing.T) {
				w := newExecutionWorld(t, direction)
				progress := &faultExecutionProgress{ExecutionProgressStore: w.db.Executions(), inTx: w.db.InTx, effectError: stage == "effect"}
				w.projectWith(progress)
				if direction == model.ReplicationPush && stage != "effect" {
					w.push.push.Store = executionPushFaultStore{
						PushStore: w.push.push.Store,
						progress:  executionPushFaultProgress{PushProgressStore: w.db.PushProgress(), stage: stage},
					}
				}
				if direction == model.ReplicationPull && stage != "effect" {
					w.pull.driver.Store = pullFaultStore{
						PullStore: w.pull.driver.Store,
						progress:  pullFaultProgress{PullProgressStore: w.db.PullProgress(), mode: stage},
					}
				}
				if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrStore) {
					t.Fatal("failed observed write became a successful execution", err)
				}
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				want := model.ExecutionOutcomes{}
				if stage == "receipt" {
					want.Pending = 1
				}
				if current.Phase != model.ExecutionRunning || current.Outcomes != want || !current.Completed.IsZero() {
					t.Fatal("failed checkpoint retained false or orphaned execution evidence", current.Phase, current.Outcomes)
				}
				if stage != "receipt" && (direction == model.ReplicationPush && w.push.puts != 0 || direction == model.ReplicationPull && w.pull.fetches != 0) {
					t.Fatal("rejected intent/cursor reached an artifact effect")
				}
				if stage == "receipt" && direction == model.ReplicationPush && (w.push.puts == 0 || w.push.targetBodies["selected/file"] != "verified bytes") {
					t.Fatal("receipt fault did not exercise the acknowledged remote write")
				}
				if stage == "receipt" && direction == model.ReplicationPull {
					var cached bool
					if err := w.pull.platform.Settings().Get(t.Context(), "cache-effect:selected/file", &cached); !errors.Is(err, kernel.ErrNotFound) {
						t.Fatal("cache mutation escaped its rejected atomic receipt", err)
					}
				}
				progress.effectError = false
				if direction == model.ReplicationPush {
					w.push.push.Store = w.db.Store
				} else {
					w.pull.driver.Store = w.db.Store
				}
				must(t, w.worker.Execute(t.Context(), w.item))
				current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
					t.Fatal("retry lost the real effect or double-counted its rejected projection", current.Outcomes)
				}
			})
		}
	}
}

func TestUnitReplicationTerminalProjectionFailureKeepsTraversalAndStatusIncomplete(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		for _, stage := range []string{"read", "advance"} {
			t.Run(string(direction)+"/"+stage, func(t *testing.T) {
				w := newExecutionWorld(t, direction)
				progress := &faultExecutionProgress{ExecutionProgressStore: w.db.Executions(), inTx: w.db.InTx}
				if stage == "read" {
					progress.lockedReadPhase = model.ExecutionRunning
				} else {
					progress.advanceFailurePhase = model.ExecutionComplete
				}
				w.projectWith(progress)
				if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrStore) {
					t.Fatal("terminal projection failure became successful completion", err)
				}
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Status() != model.ReplicationIncomplete || !current.Completed.IsZero() || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
					t.Fatal("retained successful bytes became fabricated terminal proof", current.Phase, current.Outcomes)
				}
				if direction == model.ReplicationPush {
					transfer, err := w.db.PushProgress().Run(t.Context(), model.PushReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation})
					must(t, err)
					if transfer.Phase.Terminal() {
						t.Fatal("direction completion escaped rejected execution settlement")
					}
				} else {
					transfer, err := w.db.PullProgress().Run(t.Context(), model.PullReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation})
					must(t, err)
					if transfer.Phase.Terminal() {
						t.Fatal("pull completion escaped rejected execution settlement")
					}
				}
				progress.lockedReadPhase, progress.advanceFailurePhase = "", ""
				must(t, w.worker.Execute(t.Context(), w.item))
				current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) || current.Completed.IsZero() {
					t.Fatal("retry did not join terminal traversal and truthful execution status")
				}
			})
		}
	}
}

func TestUnitReplicationCancellationAfterCheckpointRetainsResumableEvidence(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPush)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	step := w.worker.Drivers[model.ReplicationPush]
	w.worker.Drivers[model.ReplicationPush] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
		if err := step(ctx, run, fence); err != nil {
			return err
		}
		cancel()
		return nil
	}
	w.item.FinalAttempt = true
	if err := w.worker.Execute(ctx, w.item); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown after a committed checkpoint lost cancellation identity", err)
	}
	current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Phase != model.ExecutionRunning || current.Outcomes != (model.ExecutionOutcomes{Pending: 1}) || !current.Completed.IsZero() || w.push.puts != 0 {
		t.Fatal("shutdown settled or discarded its unfinished selected checkpoint", current.Phase, current.Outcomes)
	}
	w.worker.Drivers[model.ReplicationPush] = step
	w.item.FinalAttempt = false
	must(t, w.worker.Execute(t.Context(), w.item))
	current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("resumed shutdown checkpoint lost or duplicated selected effects")
	}
}
