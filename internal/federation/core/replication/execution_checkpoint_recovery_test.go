package replication_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type executionPushReadbackFault struct {
	port.PushProgressStore
	inTx              func(context.Context) bool
	committed, failed bool
}

func (p *executionPushReadbackFault) Advance(ctx context.Context, version int64, run model.PushRun) error {
	if err := p.PushProgressStore.Advance(ctx, version, run); err != nil {
		return err
	}
	if !p.failed {
		p.committed = true
	}
	return nil
}

func (p *executionPushReadbackFault) Run(ctx context.Context, ref model.PushReference) (model.PushRun, error) {
	if p.committed && !p.inTx(ctx) {
		p.committed, p.failed = false, true
		return model.PushRun{}, errExecutionStorage
	}
	return p.PushProgressStore.Run(ctx, ref)
}

type executionPullReadbackFault struct {
	port.PullProgressStore
	inTx              func(context.Context) bool
	committed, failed bool
}

func (p *executionPullReadbackFault) Advance(ctx context.Context, version int64, run model.PullRun) error {
	if err := p.PullProgressStore.Advance(ctx, version, run); err != nil {
		return err
	}
	if !p.failed {
		p.committed = true
	}
	return nil
}

func (p *executionPullReadbackFault) Run(ctx context.Context, ref model.PullReference) (model.PullRun, error) {
	if p.committed && !p.inTx(ctx) {
		p.committed, p.failed = false, true
		return model.PullRun{}, errExecutionStorage
	}
	return p.PullProgressStore.Run(ctx, ref)
}

type executionPullItemReadFault struct{ port.PullProgressStore }

func (p executionPullItemReadFault) Item(context.Context, model.PullReference, model.PullAction, string) (model.PullItem, error) {
	return model.PullItem{}, errExecutionStorage
}

func TestUnitReplicationCheckpointReadbackFailureKeepsCommittedIntentResumable(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		t.Run(string(direction), func(t *testing.T) {
			w := newExecutionWorld(t, direction)
			if direction == model.ReplicationPush {
				w.push.push.Store = executionPushFaultStore{PushStore: w.db.Store, progress: &executionPushReadbackFault{PushProgressStore: w.db.PushProgress(), inTx: w.db.InTx}}
			} else {
				w.pull.driver.Store = pullFaultStore{PullStore: w.db.Store, progress: &executionPullReadbackFault{PullProgressStore: w.db.PullProgress(), inTx: w.db.InTx}}
			}
			if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("checkpoint readback failure became completed dispatch", err)
			}
			current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Phase != model.ExecutionRunning || current.Outcomes != (model.ExecutionOutcomes{Pending: 1}) || !current.Completed.IsZero() {
				t.Fatal("committed checkpoint was lost or misreported after readback failure", current.Outcomes)
			}
			if direction == model.ReplicationPush && w.push.puts != 0 || direction == model.ReplicationPull && w.pull.fetches != 0 {
				t.Fatal("readback failure advanced into an unacknowledged next effect")
			}
			if direction == model.ReplicationPush {
				w.push.push.Store = w.db.Store
			} else {
				w.pull.driver.Store = w.db.Store
			}
			must(t, w.worker.Execute(t.Context(), w.item))
			current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
				t.Fatal("checkpoint retry lost or repeated its committed selected intent")
			}
		})
	}
}

func TestUnitReplicationPullReceiptCannotBypassUnavailablePrivateIntent(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPull)
	w.pull.driver.Store = pullFaultStore{PullStore: w.db.Store, progress: executionPullItemReadFault{w.db.PullProgress()}}
	if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unavailable private intent authorized owner cache publication", err)
	}
	current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Outcomes != (model.ExecutionOutcomes{Pending: 1}) || current.Phase != model.ExecutionRunning {
		t.Fatal("unavailable receipt binding lost selected pending proof")
	}
	var cached bool
	if err := w.pull.platform.Settings().Get(t.Context(), "cache-effect:selected/file", &cached); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("receipt binding read failure committed a cache effect", err)
	}
	w.pull.driver.Store = w.db.Store
	must(t, w.worker.Execute(t.Context(), w.item))
	current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("receipt-binding recovery did not settle the original intent once")
	}
}
