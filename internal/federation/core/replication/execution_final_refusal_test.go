package replication_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type executionPullOwnerFault struct {
	artifactsapi.PullSource
	failure error
	reads   int
}

func (p *executionPullOwnerFault) Repository(ctx context.Context, key string, selection artifactsapi.PullSelection) (artifactsapi.PullRepository, error) {
	p.reads++
	if p.failure != nil {
		return artifactsapi.PullRepository{}, p.failure
	}
	return p.PullSource.Repository(ctx, key, selection)
}

func TestUnitReplicationPullOwnerRefusalDoesNotReserveAdmissionOrAuthorizeResume(t *testing.T) {
	for _, stage := range []string{"admission", "resumption"} {
		t.Run(stage, func(t *testing.T) {
			w := newExecutionWorld(t, model.ReplicationPull)
			owner := &executionPullOwnerFault{PullSource: w.pull.driver.Source, failure: artifactsapi.ErrPullOffline}
			w.pull.driver.Source = owner
			if stage == "admission" {
				ctx, err := w.pull.driver.Restore(t.Context(), w.run.Binding.Credential)
				must(t, err)
				request := replication.PullRequest{
					ID:             "owner-refused-admission",
					TargetKey:      w.run.Binding.TargetKey,
					TargetRevision: w.run.Binding.TargetRevision,
					Root:           "selected",
				}
				run, err := w.pull.driver.Start(ctx, request, nil)
				if !errors.Is(err, artifactsapi.ErrPullOffline) || run.Reference != (model.PullReference{}) || owner.reads != 1 {
					t.Fatal("offline owner admitted a private transfer or was not consulted", err)
				}
				owner.failure = nil
				healthyAt := time.Unix(200, 0)
				w.pull.driver.Now = func() time.Time { return healthyAt }
				run, err = w.pull.driver.Start(ctx, request, nil)
				must(t, err)
				if run.Version != 1 || run.Reference.ID != request.ID || !run.Started.Equal(healthyAt) || !run.Updated.Equal(healthyAt) {
					t.Fatal("refused owner admission retained an earlier transfer")
				}
			} else {
				if err := w.worker.Execute(t.Context(), w.item); !errors.Is(err, artifactsapi.ErrPullOffline) || owner.reads == 0 {
					t.Fatal("resumption bypassed the current artifact owner", err)
				}
				current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Phase != model.ExecutionRunning || current.Outcomes != (model.ExecutionOutcomes{}) || w.pull.fetches != 0 {
					t.Fatal("offline owner published a selected intent or cache effect")
				}
				owner.failure = nil
				must(t, w.worker.Execute(t.Context(), w.item))
				current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
				must(t, err)
				if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
					t.Fatal("owner recovery did not complete the same logical dispatch once")
				}
			}
		})
	}
}

func TestUnitReplicationPullStatisticsRefusalPrecedesArtifactOwnerAdmission(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPull)
	must(t, w.pull.service.Update(t.Context(), "remote", replication.Input{SyncStatistics: ptr(true)}))
	target := stored(t, w.pull.world, "remote")
	owner := &executionPullOwnerFault{PullSource: w.pull.driver.Source}
	w.pull.driver.Source = owner
	ctx, err := w.pull.driver.Restore(t.Context(), w.run.Binding.Credential)
	must(t, err)
	run, err := w.pull.driver.Start(ctx, replication.PullRequest{ID: "unsupported-statistics", TargetKey: target.Key, TargetRevision: target.Revision, Root: "selected"}, nil)
	if !errors.Is(err, replication.ErrPushStatistics) || run.Reference != (model.PullReference{}) || owner.reads != 0 || w.pull.fetches != 0 {
		t.Fatal("unsupported statistics reached owner selection or admitted transfer", err)
	}
	retained, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if !reflect.DeepEqual(retained, w.run) {
		t.Fatal("unsupported capability rewrote the existing queued execution")
	}
}

func TestUnitReplicationExhaustedTransferCannotSettleAfterProjectGuardRevocation(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPull)
	w.item.FinalAttempt = true
	w.pull.fetchError = artifactsapi.ErrPullOffline
	var denied atomic.Bool
	guard := w.worker.Configurations.Guard
	w.worker.Configurations.Guard = func(ctx context.Context) error {
		if denied.Load() {
			return kernel.ErrForbidden
		}
		return guard(ctx)
	}
	step := w.worker.Drivers[model.ReplicationPull]
	w.worker.Drivers[model.ReplicationPull] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
		err := step(ctx, run, fence)
		if err != nil {
			denied.Store(true)
		}
		return err
	}
	err := w.worker.Execute(t.Context(), w.item)
	if !errors.Is(err, artifactsapi.ErrPullOffline) || !errors.Is(err, kernel.ErrForbidden) || !errors.Is(err, work.ErrUnsettled) {
		t.Fatal("revoked final settlement parked truthful unfinished execution", err)
	}
	current, err := w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Phase != model.ExecutionRunning || current.Failure != "" || current.Outcomes != (model.ExecutionOutcomes{Pending: 1}) || !current.Completed.IsZero() {
		t.Fatal("failed final settlement fabricated terminal provenance", current.Phase, current.Outcomes)
	}
	denied.Store(false)
	w.pull.fetchError = nil
	must(t, w.worker.Execute(t.Context(), w.item))
	current, err = w.db.Executions().Run(t.Context(), w.run.Reference)
	must(t, err)
	if current.Status() != model.ReplicationOK || current.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("restored guard could not recover unfinished final attempt once")
	}
}

func TestUnitReplicationOwnershipGuardRefusalCannotAllocateTargetOrEnterTransfer(t *testing.T) {
	db, ownership, _, items := ownershipFixture(t)
	guard := ownership.Guard
	ownership.Guard = func(context.Context) error { return kernel.ErrForbidden }
	entered := false
	err := ownership.Run(t.Context(), items[0], "target", func(context.Context, replication.Owner) error { entered = true; return nil })
	if !errors.Is(err, kernel.ErrForbidden) || entered {
		t.Fatal("revoked project guard allocated execution authority", err)
	}
	stats, err := db.WorkQueue().Stats(t.Context())
	must(t, err)
	for _, state := range stats {
		if state.Kind == replication.TargetWorkKind {
			t.Fatal("revoked guard allocated a target reservation")
		}
	}
	retained, err := db.WorkQueue().Get(t.Context(), items[0].Kind, items[0].Key)
	must(t, err)
	if !reflect.DeepEqual(retained, items[0]) {
		t.Fatal("revoked target admission rewrote the run lease")
	}
	ownership.Guard = guard
	owner, err := ownership.Claim(t.Context(), items[0], "target")
	must(t, err)
	must(t, ownership.Release(t.Context(), owner))
}

func TestUnitReplicationOccurrenceCannotRestorePrivateAuthorInsideMetadataTransaction(t *testing.T) {
	w, db, coordinator, ctx := coordinatorFixture(t, "local", true)
	target := stored(t, w, "local")
	trigger := schedule.Trigger{
		Reference: schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key},
		Revision:  target.Revision,
		Fire:      time.Unix(1, 0),
		ID:        "transactional-occurrence",
		Fence:     func(context.Context) error { return nil },
	}
	restored := false
	restore := coordinator.Restore
	coordinator.Restore = func(ctx context.Context, ref kernel.CredentialReference) (context.Context, error) {
		restored = true
		return restore(ctx, ref)
	}
	err := db.Tx(ctx, func(ctx context.Context) error { _, err := coordinator.Scheduled(ctx, trigger); return err })
	if !errors.Is(err, kernel.ErrInvalid) || restored {
		t.Fatal("transactional occurrence restored credentials or admitted work", err)
	}
	_, err = db.Executions().ForOccurrence(ctx, trigger.ID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("refused transactional occurrence retained execution", err)
	}
	run, err := coordinator.Scheduled(t.Context(), trigger)
	must(t, err)
	if !restored || run.Binding.Occurrence != trigger.ID {
		t.Fatal("healthy occurrence could not recover the same logical fire")
	}
}

func TestUnitReplicationCompletedOccurrenceReplayCannotRequeueOrRepeatEffects(t *testing.T) {
	w := newExecutionWorld(t, model.ReplicationPush)
	must(t, w.worker.Execute(t.Context(), w.item))
	must(t, w.db.WorkQueue().Complete(t.Context(), w.item.Kind, w.item.Key, w.item.Generation, time.Unix(100, 0)))
	ctx, err := w.push.push.Restore(t.Context(), w.run.Binding.Credential)
	must(t, err)
	must(t, w.push.service.Update(ctx, "local", replication.Input{CronExp: ptr("0 0 2 * * ?")}))
	target := stored(t, w.push.world, "local")
	coordinator := replication.Coordinator{
		Store:          w.db,
		Configurations: w.push.service,
		Restore:        w.push.push.Restore,
		Now:            func() time.Time { return time.Unix(100, 0) },
		Drivers: map[model.ReplicationDirection]replication.TransferStart{
			model.ReplicationPush: func(ctx context.Context, request replication.TransferRequest) (replication.TransferAdmission, error) {
				run, err := w.push.push.Start(
					ctx,
					replication.PushRequest{ID: request.ID, TargetKey: request.TargetKey, TargetRevision: request.TargetRevision, Root: request.Root},
					nil,
				)
				return replication.TransferAdmission{ID: run.Reference.ID, Incarnation: run.Reference.Incarnation, Repository: run.Binding.Source}, err
			},
		},
	}
	trigger := schedule.Trigger{
		Reference: schedule.Reference{Kind: replication.ScheduleKind, Key: target.Key},
		Revision:  target.Revision,
		Fire:      time.Unix(100, 0),
		ID:        "completed-occurrence",
		Fence:     func(context.Context) error { return nil },
	}
	admitted, err := coordinator.Scheduled(t.Context(), trigger)
	must(t, err)
	item := leaseExecution(t, w.db)
	must(t, w.worker.Execute(t.Context(), item))
	completed, err := w.db.Executions().Run(t.Context(), admitted.Reference)
	must(t, err)
	if completed.Status() != model.ReplicationOK || completed.Outcomes != (model.ExecutionOutcomes{Succeeded: 1}) {
		t.Fatal("control did not finish actual scheduled transfer")
	}
	must(t, w.db.WorkQueue().Complete(t.Context(), item.Kind, item.Key, item.Generation, time.Unix(100, 0)))
	writes := w.push.puts + w.push.patches + w.push.deletes
	replayed, err := coordinator.Scheduled(t.Context(), trigger)
	must(t, err)
	if !reflect.DeepEqual(replayed, completed) || w.push.puts+w.push.patches+w.push.deletes != writes {
		t.Fatal("completed occurrence replay changed retained proof or repeated peer effects")
	}
	if _, err := w.db.WorkQueue().Get(t.Context(), item.Kind, item.Key); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("completed occurrence replay requeued a terminal execution", err)
	}
}
