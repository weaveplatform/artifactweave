package replication_test

import (
	"context"
	"errors"
	"io"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type executionPushSourceFault struct {
	artifactsapi.TransferSource
	world *pushWorld
	stage string
}

type executionPushCloseFault struct{ io.ReadSeekCloser }

func (r executionPushCloseFault) Close() error {
	return errors.Join(r.ReadSeekCloser.Close(), errExecutionStorage)
}

func (s *executionPushSourceFault) Open(ctx context.Context, item artifactsapi.TransferItem) (io.ReadSeekCloser, error) {
	if s.stage == "open" {
		return nil, errExecutionStorage
	}
	body, err := s.TransferSource.Open(ctx, item)
	if err != nil {
		return nil, err
	}
	if s.stage == "close" {
		return executionPushCloseFault{body}, nil
	}
	if s.stage == "changed during open" {
		s.world.addSource(item.Path, "source changed while body opened")
	}
	return body, nil
}

func TestUnitReplicationPushSourceIOFailureRetainsUncertainEffectForSafeRecovery(t *testing.T) {
	for _, stage := range []string{"open", "close", "changed during open"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			source := &executionPushSourceFault{TransferSource: w.push.push.Source, world: w.push, stage: stage}
			w.push.push.Source = source
			var want error = errExecutionStorage
			if stage == "changed during open" {
				want = kernel.ErrConflict
			}
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, want) {
				t.Fatal("source I/O failure became completed dispatch", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase != model.ExecutionRunning || current.Outcomes != (model.ExecutionOutcomes{Pending: 1}) || !current.Completed.IsZero() {
				t.Fatal("uncertain source effect was lost or counted as successful", current.Outcomes)
			}
			if stage == "close" {
				if w.push.puts != 1 || w.push.targetBodies["selected/file"] != "verified bytes" || w.push.closes != 1 {
					t.Fatal("close failure did not follow acknowledged bytes and release the source reader")
				}
			} else if w.push.puts != 0 {
				t.Fatal("unavailable or changed source reached peer publication")
			}
			source.stage = ""
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if stage == "changed during open" {
				if current.Status() != model.ReplicationWarn || current.Outcomes != (model.ExecutionOutcomes{Skipped: 1}) || w.push.puts != 0 {
					t.Fatal("obsolete source snapshot became successful bytes")
				}
			} else if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) || w.push.puts != 1 || w.push.closes != 1 {
				t.Fatal("source I/O recovery repeated acknowledged bytes or lost completion")
			}
		})
	}
}

func TestUnitReplicationPushConcurrentTargetChangesRemainWarningWithoutOverwrite(t *testing.T) {
	for _, phase := range []model.PushItemPhase{model.PushContentVerified, model.PushMetadataVerified} {
		t.Run(string(phase), func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPush)
			step := w.worker.Drivers[model.ReplicationPush]
			changed := false
			w.worker.Drivers[model.ReplicationPush] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
				if err := step(ctx, run, fence); err != nil {
					return err
				}
				item, err := w.db.PushProgress().
					Item(ctx, model.PushReference{ID: run.Binding.TransferID, Incarnation: run.Binding.TransferIncarnation}, model.PushUpload, "selected/file")
				if err == nil && !changed && item.Phase == phase {
					changed = true
					if phase == model.PushContentVerified {
						w.push.addTarget("selected/file", "concurrent destination bytes")
					} else {
						target := w.push.target["selected/file"]
						target.Properties = map[string][]string{"concurrent": {"destination"}}
						w.push.target["selected/file"] = target
					}
				} else if err != nil && !errors.Is(err, kernel.ErrNotFound) {
					return err
				}
				return nil
			}
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if !changed || current.Status() != model.ReplicationWarn || current.Outcomes != (model.ExecutionOutcomes{Skipped: 1}) {
				t.Fatal("concurrent destination replacement became successful dispatch", current.Outcomes)
			}
			if phase == model.PushContentVerified && w.push.targetBodies["selected/file"] != "concurrent destination bytes" {
				t.Fatal("concurrent destination bytes were overwritten")
			}
			if phase == model.PushMetadataVerified && len(w.push.target["selected/file"].Properties["concurrent"]) != 1 {
				t.Fatal("concurrent destination metadata was overwritten")
			}
		})
	}
}
