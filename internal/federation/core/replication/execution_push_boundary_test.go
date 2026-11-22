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

type executionPushObservationFault struct {
	port.PushConnection
	world *pushWorld
	stage string
}

func (c executionPushObservationFault) Inspect(ctx context.Context, path string) (port.PushTargetItem, error) {
	if c.world.puts > 0 && c.stage == "post-upload missing" {
		return port.PushTargetItem{}, kernel.ErrNotFound
	}
	if c.world.puts > 0 && c.stage == "post-upload denied" || c.world.patches > 0 && c.stage == "post-properties denied" ||
		path == "selected/gone" && (c.stage == "delete inspect denied" || c.world.deletes > 0 && c.stage == "post-delete denied") {
		return port.PushTargetItem{}, port.PushTargetForbidden
	}
	return c.PushConnection.Inspect(ctx, path)
}

func (c executionPushObservationFault) Properties(ctx context.Context, path string, values map[string][]string, before port.PushMutationFence) error {
	if c.stage == "properties not applied" {
		if err := before(ctx); err != nil {
			return err
		}
		c.world.patches++
		return nil // Model a peer acknowledgement without the requested metadata.
	}
	return c.PushConnection.Properties(ctx, path, values, before)
}

func (c executionPushObservationFault) Delete(ctx context.Context, path string, expected model.PushTargetSnapshot, before port.PushMutationFence) error {
	if c.stage == "delete conflict" {
		return kernel.ErrConflict
	}
	if c.stage == "revoked before delete" {
		c.world.policy = authority{}
	}
	return c.PushConnection.Delete(ctx, path, expected, before)
}

func TestUnitReplicationPushRevocationAroundDeleteRetainsUncertainRemovalForRetry(t *testing.T) {
	for _, stage := range []string{"revoked before delete", "revoked after delete"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorldWithOptions(t, model.ReplicationPush, &replication.ExecutionInput{SyncDeletes: ptr(true)})
			w.push.addTarget("selected/gone", "original destination")
			policy := w.push.policy
			connections := w.push.push.Connections
			w.push.push.Connections = pushConnectionsFunc(func(ctx context.Context, target model.Replication) (port.PushConnection, error) {
				connection, err := connections.Connection(ctx, target)
				return executionPushObservationFault{PushConnection: connection, world: w.push, stage: stage}, err
			})
			if stage == "revoked after delete" {
				w.push.deleteAfter = func() { w.push.policy = authority{} }
			}
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrForbidden) {
				t.Fatal("revoked deletion retained current publication authority", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase != model.ExecutionRunning || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1, Pending: 1}) || !current.Completed.IsZero() {
				t.Fatal("revoked deletion fabricated completion or lost successful upload proof", current.Outcomes)
			}
			if stage == "revoked before delete" && (w.push.deletes != 0 || w.push.targetBodies["selected/gone"] != "original destination") {
				t.Fatal("last-moment deletion refusal still changed destination")
			}
			if stage == "revoked after delete" && w.push.deletes != 1 {
				t.Fatal("acknowledgement control did not reach actual deletion")
			}
			w.push.policy, w.push.push.Connections, w.push.deleteAfter = policy, connections, nil
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 2}) || w.push.deletes != 1 {
				t.Fatal("authorized retry lost or repeated acknowledged deletion", current.Outcomes)
			}
		})
	}
}

func TestUnitReplicationPushUnverifiedPeerAcknowledgementsCannotBecomeSuccess(t *testing.T) {
	for _, stage := range []string{"post-upload missing", "post-upload denied", "post-properties denied", "properties not applied"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			if stage == "post-properties denied" || stage == "properties not applied" {
				w.push.addTarget("selected/file", "verified bytes")
			}
			original := w.push.push.Connections
			w.push.push.Connections = pushConnectionsFunc(func(ctx context.Context, target model.Replication) (port.PushConnection, error) {
				connection, err := original.Connection(ctx, target)
				return executionPushObservationFault{PushConnection: connection, world: w.push, stage: stage}, err
			})
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase != model.ExecutionComplete || current.Status() != model.ReplicationError || current.Outcomes != (model.ExecutionOutcomes{Failed: 1}) {
				t.Fatal("unverified peer response became a successful execution", current.Phase, current.Status(), current.Outcomes)
			}
			item, err := w.db.PushProgress().
				Item(t.Context(), model.PushReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation}, model.PushUpload, "selected/file")
			must(t, err)
			if item.Outcome != model.PushItemFailed || item.Reason == "" || w.push.targetBodies["selected/file"] != "verified bytes" {
				t.Fatal("control did not reach acknowledged content or retain closed failure proof")
			}
			before := w.push.puts + w.push.patches
			must(t, w.worker.Execute(t.Context(), w.item))
			if w.push.puts+w.push.patches != before {
				t.Fatal("terminal failure replay repeated remote work")
			}
		})
	}
}

func TestUnitReplicationPushDeletionRefusalsRetainTruthfulEffectCounts(t *testing.T) {
	for _, stage := range []string{"delete inspect denied", "delete conflict", "post-delete denied", "target reappeared", "source reappeared"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorldWithOptions(t, model.ReplicationPush, &replication.ExecutionInput{SyncDeletes: ptr(true)})
			w.push.addTarget("selected/gone", "original destination")
			original := w.push.push.Connections
			w.push.push.Connections = pushConnectionsFunc(func(ctx context.Context, target model.Replication) (port.PushConnection, error) {
				connection, err := original.Connection(ctx, target)
				return executionPushObservationFault{PushConnection: connection, world: w.push, stage: stage}, err
			})
			if stage == "target reappeared" {
				w.push.deleteAfter = func() { w.push.addTarget("selected/gone", "concurrent replacement") }
			}
			if stage == "source reappeared" {
				step := w.worker.Drivers[model.ReplicationPush]
				w.worker.Drivers[model.ReplicationPush] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
					if err := step(ctx, run, fence); err != nil {
						return err
					}
					_, err := w.db.PushProgress().
						Item(ctx, model.PushReference{ID: run.Binding.TransferID, Incarnation: run.Binding.TransferIncarnation}, model.PushDelete, "selected/gone")
					if err == nil {
						w.push.addSource("selected/gone", "new source protects destination")
					} else if !errors.Is(err, kernel.ErrNotFound) {
						return err
					}
					return nil
				}
			}
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			want, status := model.ExecutionOutcomes{Succeeded: 1, Skipped: 1}, model.ReplicationWarn
			if stage == "delete inspect denied" || stage == "post-delete denied" {
				want, status = model.ExecutionOutcomes{Succeeded: 1, Failed: 1}, model.ReplicationPartialFailure
			}
			if current.Phase != model.ExecutionComplete || current.Outcomes != want || current.Status() != status {
				t.Fatal("unverified deletion became success or lost a verified upload", current.Outcomes, current.Status())
			}
			item, err := w.db.PushProgress().
				Item(t.Context(), model.PushReference{ID: w.run.Binding.TransferID, Incarnation: w.run.Binding.TransferIncarnation}, model.PushDelete, "selected/gone")
			must(t, err)
			if item.TargetAbsent || item.VerifiedTarget != nil || item.Reason == "" {
				t.Fatal("refused deletion fabricated acknowledged absence")
			}
			if (stage == "delete conflict" || stage == "source reappeared" || stage == "delete inspect denied") &&
				(w.push.deletes != 0 || w.push.targetBodies["selected/gone"] != "original destination") {
				t.Fatal("protected destination was deleted")
			}
			if stage == "target reappeared" && w.push.targetBodies["selected/gone"] != "concurrent replacement" {
				t.Fatal("concurrent destination replacement was lost")
			}
		})
	}
}
