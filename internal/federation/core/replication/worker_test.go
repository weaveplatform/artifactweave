package replication_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type executionWorld struct {
	db     dispatchStore
	worker replication.Worker
	run    model.ExecutionRun
	item   work.WorkItem
	push   *pushWorld
	pull   *pullWorld
}

func newExecutionWorld(t *testing.T, direction model.ReplicationDirection) *executionWorld {
	t.Helper()
	return newExecutionWorldWithOptions(t, direction, nil)
}

func newExecutionWorldWithOptions(t *testing.T, direction model.ReplicationDirection, input *replication.ExecutionInput) *executionWorld {
	t.Helper()
	return newExecutionWorldWithLimit(t, direction, input, 0)
}

func newExecutionWorldWithLimit(t *testing.T, direction model.ReplicationDirection, input *replication.ExecutionInput, limit int) *executionWorld {
	t.Helper()
	out := &executionWorld{}
	var core *world
	var ctx context.Context
	var repository string
	var start replication.TransferStart
	var step replication.TransferStep
	var restore func(context.Context, kernel.CredentialReference) (context.Context, error)
	if direction == model.ReplicationPush {
		p := newPushWorld(t)
		out.push, core, repository = p, p.world, "local"
		p.addSource("selected/file", "verified bytes")
		ctx = accessapi.WithCaller(t.Context(), kernel.Principal{Name: "original", TokenID: "verified-token"}, nil)
		start = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
			driver := p.push
			driver.Configurations = driver.Configurations.WithExecutionOptions(request.Options)
			run, err := driver.Start(ctx, replication.PushRequest{ID: request.ID, TargetKey: request.TargetKey, TargetRevision: request.TargetRevision, Root: request.Root}, nil)
			return replication.TransferAdmission{ID: run.Reference.ID, Incarnation: run.Reference.Incarnation, Repository: run.Binding.Source}, err
		}
		step = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
			return p.push.ExecutionStep(ctx, run, out.db.Executions(), fence)
		}
		restore = p.push.Restore
	} else {
		p, authorized, existing := newPullWorld(t, false)
		out.pull, core, repository, ctx = p, p.world, "remote", authorized
		p.add(existing, "selected/file")
		start = func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
			driver := p.driver
			driver.Configurations = driver.Configurations.WithExecutionOptions(request.Options)
			run, err := driver.Start(ctx, replication.PullRequest{ID: request.ID, TargetKey: request.TargetKey, TargetRevision: request.TargetRevision, Root: request.Root}, nil)
			if err == nil {
				for index := range p.items {
					p.items[index].Selection = run.Binding.Selection()
				}
			}
			return replication.TransferAdmission{ID: run.Reference.ID, Incarnation: run.Reference.Incarnation, Repository: run.Binding.Source.Repository}, err
		}
		step = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
			return p.driver.ExecutionStep(ctx, run, out.db.Executions(), fence)
		}
		restore = p.driver.Restore
	}
	out.db = dispatchStore{Store: core.db, platform: platforminmem.New(core.db.Store)}
	if limit > 0 {
		core.policy = authority{any: true, project: core.repos.values[repository].ProjectKey}
		core.service.UserQueryLimit = limit
		if out.push != nil {
			out.push.push.Configurations = core.service
		} else {
			out.pull.driver.Configurations = core.service
		}
	}
	now := func() time.Time { return time.Unix(100, 0) }
	coordinator := replication.Coordinator{
		Store: out.db, Configurations: core.service, Drivers: map[model.ReplicationDirection]replication.TransferStart{direction: start}, Restore: restore, Now: now,
	}
	var run model.ExecutionRun
	var err error
	if input == nil {
		run, err = coordinator.Execute(ctx, repository, "selected")
	} else {
		run, err = coordinator.ExecuteWithOptions(ctx, repository, "selected", *input)
	}
	must(t, err)
	out.run = run
	out.worker = replication.Worker{
		Store: out.db, Configurations: core.service, Drivers: map[model.ReplicationDirection]replication.TransferStep{direction: step}, Restore: restore, Now: now,
	}
	out.item = leaseExecution(t, out.db)
	return out
}

func leaseExecution(t *testing.T, db dispatchStore) work.WorkItem {
	t.Helper()
	items, err := db.WorkQueue().Lease(t.Context(), work.WorkLease{
		Kinds: []string{replication.ExecutionWorkKind}, Holder: "worker", Limit: 1, Now: time.Unix(100, 0), Until: time.Unix(200, 0),
	})
	must(t, err)
	if len(items) != 1 {
		t.Fatal("logical execution was not durably queued")
	}
	return items[0]
}

func TestUnitReplicationWorkerProjectsActualPushAndPullCheckpoints(t *testing.T) {
	for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
		t.Run(string(direction), func(t *testing.T) {
			w := newExecutionWorld(t, direction)
			must(t, w.worker.Execute(t.Context(), w.item))
			run, err := w.db.Executions().Run(t.Context(), w.run.Reference)
			must(t, err)
			if run.Phase != model.ExecutionComplete || run.Status() != model.ReplicationOK || run.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
				t.Fatal("driver completion disagreed with actual execution evidence", run.Phase, run.Status(), run.Outcomes)
			}
			if direction == model.ReplicationPush && w.push.targetBodies["selected/file"] != "verified bytes" {
				t.Fatal("completed push did not change destination bytes")
			}
			if direction == model.ReplicationPull && w.pull.fetches != 1 {
				t.Fatal("completed pull did not commit exactly one owner receipt")
			}
			state, err := w.db.Executions().PathStatus(t.Context(), run.Binding.TargetKey, run.Binding.Repository.Incarnation, "selected")
			must(t, err)
			if state.Latest.Status != model.ReplicationOK || state.LastCompleted.IsZero() {
				t.Fatal("committed effects did not produce scoped completion")
			}
		})
	}
}

func TestUnitReplicationWorkerRetryResumesPendingProofAndSanitizesQueueErrors(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPush)
	failure := errors.New("private upstream diagnostic must not be retained")
	w.push.putError = failure
	err := w.worker.Execute(t.Context(), w.item)
	if !errors.Is(err, failure) || strings.Contains(err.Error(), "private upstream") {
		t.Fatal("retry identity was lost or queue diagnostic was disclosed", err)
	}
	interrupted, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if interrupted.Status() != model.ReplicationIncomplete || interrupted.Outcomes != (model.ExecutionOutcomes{Pending: 1}) {
		t.Fatal("temporary failed transfer became successful or lost its pending intent")
	}
	must(t, w.db.WorkQueue().Release(t.Context(), w.item.Kind, w.item.Key, w.item.Generation, time.Unix(100, 0)))
	w.item = leaseExecution(t, w.db)
	w.push.putError = nil
	must(t, w.worker.Execute(t.Context(), w.item))
	completed, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if completed.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) || completed.Status() != model.ReplicationOK {
		t.Fatal("retry double-counted selected progress or failed to settle")
	}
}

func TestUnitReplicationWorkerExhaustionSettlesFailureWithoutFalseSuccess(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPull)
	w.pull.fetchError = errors.New("temporary peer failure with private diagnostic")
	w.item.FinalAttempt = true
	must(t, w.worker.Execute(t.Context(), w.item))
	run, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if run.Phase != model.ExecutionAborted || run.Failure != model.ExecutionRetryExhausted || run.Status() != model.ReplicationError || run.Outcomes.Succeeded != 0 ||
		run.Outcomes.Pending != 1 {
		t.Fatal("exhausted transfer fabricated terminal success")
	}
}

func TestUnitReplicationWorkerRechecksAdministrationAtBlockedPublication(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPull)
	w.pull.beforeFetch = func() { w.pull.policy = authority{} }
	if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("lost replication administration retained publication authority", err)
	}
	run, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if run.Outcomes.Succeeded != 0 || run.Phase == model.ExecutionComplete {
		t.Fatal("blocked transfer published after management revocation")
	}
}
