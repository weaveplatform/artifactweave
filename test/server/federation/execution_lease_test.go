package federation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// Use the real Quartz dispatcher, SQL queue generations and direction drivers.
// Advancing the clock expires an attempt after its first durable checkpoint;
// the successor must retain that progress without allowing stale publication.
func TestAccSingleReplicationQuartzManualAndExpiredSQLLease(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, direction := range []model.ReplicationDirection{model.ReplicationPush, model.ReplicationPull} {
			t.Run(backend+"/"+string(direction), func(t *testing.T) {
				var w *pushWorld
				repository, destination, path, content := "source", "target", pullFaultPath, pullFaultBody
				if direction == model.ReplicationPush {
					w = newPushBackendWorld(t, backend)
					w.request(w.source, http.MethodPut, itemRoute(repository, path), content, http.StatusCreated)
					w.configure("**/*.bin", "", false, false)
				} else {
					w, _, _ = newPullDriverWorld(t, backend, false)
					repository, destination = "pull-remote", "pull-remote-cache"
					w.request(w.source, http.MethodPost, replicationPath+repository, `{"cronExp":"0 0 2 * * ?"}`, http.StatusOK)
				}
				targets, err := w.runtime.Store.Replications().ForRepo(t.Context(), repository)
				if err != nil || len(targets) != 1 {
					t.Fatal("read scheduled target", err)
				}
				position, err := w.runtime.Store.Schedules().Get(t.Context(), schedule.Reference{Kind: replication.ScheduleKind, Key: targets[0].Key})
				if err != nil {
					t.Fatal(err)
				}
				// The runtime also holds the garbage collector's schedule, due
				// every four hours. This test counts and leases fires of one
				// schedule on a clock that may cross a collection fire, so
				// move collection beyond the window; otherwise its fire is
				// the "second" occurrence, depending on the time of day.
				farGC := "0 0 0 1 1 ? 2099" // Quartz's last year
				if _, err := support.StoragePolicies(w.runtime.Store).Apply(t.Context(),
					artifactsapi.StoragePolicyChange{GCCron: &farGC}, time.Now()); err != nil {
					t.Fatal(err)
				}
				var clock atomic.Int64
				clock.Store(position.Next.UnixNano())
				now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
				dispatcher := schedule.Service{Store: w.runtime.Store, Now: now}
				if count, err := dispatcher.Tick(t.Context(), 1); err != nil || count != 1 {
					t.Fatal("enqueue actual Quartz occurrence", count, err)
				}
				if count, err := dispatcher.Tick(t.Context(), 1); err != nil || count != 0 {
					t.Fatal("repeated tick invented a second logical fire", count, err)
				}
				queue := w.runtime.Store.WorkQueue()
				occurrences, err := queue.Lease(
					t.Context(),
					work.WorkLease{Kinds: []string{schedule.WorkKind}, Holder: "quartz-fixture", Limit: 1, Now: now(), Until: now().Add(time.Minute)},
				)
				if err != nil || len(occurrences) != 1 {
					t.Fatal("lease actual Quartz occurrence", err)
				}
				coordinator, err := w.runtime.Federation.Coordinator()
				if err != nil {
					t.Fatal(err)
				}
				coordinator.Now = now
				var scheduled model.ExecutionRun
				handlers := map[string]schedule.Handler{replication.ScheduleKind: func(ctx context.Context, trigger schedule.Trigger) error {
					var err error
					scheduled, err = coordinator.Scheduled(ctx, trigger)
					return err
				}}
				if err := dispatcher.Execute(t.Context(), occurrences[0], handlers); err != nil {
					t.Fatal(err)
				}
				original := scheduled.Reference
				if err := dispatcher.Execute(
					t.Context(),
					occurrences[0],
					handlers,
				); err != nil || scheduled.Reference != original ||
					scheduled.Binding.Occurrence != occurrences[0].Key {
					t.Fatal("leased occurrence replay replaced its logical run", err)
				}
				principal, err := w.runtime.Auth.Basic(t.Context(), "admin", "pw")
				if err != nil {
					t.Fatal(err)
				}
				caller := accessapi.WithCaller(t.Context(), principal, nil)
				manual, err := coordinator.Execute(caller, repository, "selected")
				if err != nil || manual.Reference == scheduled.Reference || manual.Binding.TargetLeaseKey != scheduled.Binding.TargetLeaseKey ||
					manual.Binding.TargetLeaseKey == "" {
					t.Fatal("simultaneous manual and Quartz admission lost identity or target exclusion", err)
				}
				attempts, err := queue.Lease(
					t.Context(),
					work.WorkLease{Kinds: []string{replication.ExecutionWorkKind}, Holder: "initial-fixture", Limit: 2, Now: now(), Until: now().Add(time.Minute)},
				)
				if err != nil || len(attempts) != 2 {
					t.Fatal("lease both distinct actual executions", err)
				}
				var manualItem, scheduledItem work.WorkItem
				for _, item := range attempts {
					var ref model.ExecutionReference
					if err := json.Unmarshal(item.Payload, &ref); err != nil {
						t.Fatal(err)
					}
					if ref == manual.Reference {
						manualItem = item
					} else if ref == scheduled.Reference {
						scheduledItem = item
					}
				}
				ownership := replication.Ownership{Store: w.runtime.Store, Guard: coordinator.Configurations.Guard, Now: now, Duration: time.Minute}
				oldOwner, err := ownership.Claim(t.Context(), manualItem, manual.Binding.TargetLeaseKey)
				if err != nil {
					t.Fatal(err)
				}
				worker, err := w.runtime.Federation.Worker()
				if err != nil {
					t.Fatal(err)
				}
				worker.Now, worker.Ownership = now, ownership
				if err := worker.Execute(t.Context(), scheduledItem); !errors.Is(err, kernel.ErrConflict) || !errors.Is(err, work.ErrUnsettled) {
					t.Fatal("Quartz worker bypassed the manual target reservation", err)
				}
				pending, err := w.runtime.Store.Executions().Run(t.Context(), scheduled.Reference)
				if err != nil || pending.Phase != model.ExecutionPending {
					t.Fatal("competing worker settled unexecuted transfer", err)
				}
				if direction == model.ReplicationPush {
					push, err := w.runtime.Federation.Pusher()
					if err != nil {
						t.Fatal(err)
					}
					push.Now = now
					worker.Drivers[direction] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
						return push.ExecutionStep(ctx, run, w.runtime.Store.Executions(), fence)
					}
				} else {
					pull, err := w.runtime.Federation.Puller()
					if err != nil {
						t.Fatal(err)
					}
					pull.Now = now
					worker.Drivers[direction] = func(ctx context.Context, run model.ExecutionRun, fence replication.PushFence) error {
						return pull.ExecutionStep(ctx, run, w.runtime.Store.Executions(), fence)
					}
				}
				oldFence := func(ctx context.Context) error { return ownership.Fence(ctx, oldOwner) }
				if err := worker.Drivers[direction](caller, manual, oldFence); err != nil {
					t.Fatal("publish actual first transfer checkpoint", err)
				}
				clock.Add(int64(2 * time.Minute))
				successor, taken, err := queue.LeaseKey(
					t.Context(),
					manualItem.Kind,
					manualItem.Key,
					work.WorkLease{Holder: "successor-fixture", Now: now(), Until: now().Add(time.Minute)},
				)
				if err != nil || !taken || successor.Generation <= manualItem.Generation {
					t.Fatal("SQL lease takeover did not advance the attempt", err)
				}
				newOwner, err := ownership.Claim(t.Context(), successor, manual.Binding.TargetLeaseKey)
				if err != nil || newOwner.Target.Generation <= oldOwner.Target.Generation {
					t.Fatal("target takeover did not advance the reservation", err)
				}
				assertExpiredExecutionCannotPublish(t, w, ownership, oldOwner, manual.Reference, now())
				if err := ownership.Release(t.Context(), newOwner); err != nil {
					t.Fatal(err)
				}
				if err := worker.Execute(t.Context(), successor); err != nil {
					t.Fatal("successor failed to resume actual checkpoint", err)
				}
				complete, err := w.runtime.Store.Executions().Run(t.Context(), manual.Reference)
				if err != nil || complete.Status() != model.ReplicationOK || complete.Outcomes.Succeeded != 1 {
					t.Fatal("recovered execution disagrees with actual effects", err, complete.Status(), complete.Outcomes)
				}
				scheduledItem, taken, err = queue.LeaseKey(
					t.Context(),
					scheduledItem.Kind,
					scheduledItem.Key,
					work.WorkLease{Holder: "quartz-successor", Now: now(), Until: now().Add(time.Minute)},
				)
				if err != nil || !taken {
					t.Fatal(err)
				}
				if err := worker.Execute(t.Context(), scheduledItem); err != nil {
					t.Fatal("Quartz execution did not resume after the competing owner", err)
				}
				consumer := w.target
				if direction == model.ReplicationPull {
					consumer = w.source
				}
				actual, _ := w.request(consumer, http.MethodGet, itemRoute(destination, path), "", http.StatusOK)
				if string(actual) != content {
					t.Fatal("successful SQL takeover did not produce destination bytes")
				}
				publicStatus(t, w, repository, "admin", http.StatusOK, model.ReplicationOK)
				// A stale publication after the newer completion also rolls back.
				err = w.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
					if err := coordinator.Configurations.Guard(ctx); err != nil {
						return err
					}
					if err := w.runtime.Store.Settings().Put(ctx, "replication-stale-owner-probe", true); err != nil {
						return err
					}
					return oldFence(ctx)
				})
				var probe bool
				if !errors.Is(err, kernel.ErrConflict) || !errors.Is(w.runtime.Store.Settings().Get(t.Context(), "replication-stale-owner-probe", &probe), kernel.ErrNotFound) {
					t.Fatal("expired SQL owner published after newer completion", err)
				}
				publicStatus(t, w, repository, "admin", http.StatusOK, model.ReplicationOK)
			})
		}
	}
}

func assertExpiredExecutionCannotPublish(t *testing.T, w *pushWorld, ownership replication.Ownership, oldOwner replication.Owner, ref model.ExecutionReference, at time.Time) {
	t.Helper()
	before, err := w.runtime.Store.Executions().Run(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	err = w.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
		if err := ownership.Guard(ctx); err != nil {
			return err
		}
		next := before
		next.Version++
		next.Phase, next.Failure = model.ExecutionAborted, model.ExecutionRetryExhausted
		next.Updated, next.Completed = at, at
		if err := w.runtime.Store.Executions().Advance(ctx, before.Version, next); err != nil {
			return err
		}
		return ownership.Fence(ctx, oldOwner)
	})
	after, read := w.runtime.Store.Executions().Run(t.Context(), ref)
	if !errors.Is(err, kernel.ErrConflict) || read != nil || after != before {
		t.Fatal("expired owner changed durable execution outcome", err, read)
	}
}
