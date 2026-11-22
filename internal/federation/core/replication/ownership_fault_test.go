package replication_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type ownershipFaultStore struct {
	replication.OwnershipStore
	queue work.WorkQueue
}

func (s ownershipFaultStore) WorkQueue() work.WorkQueue { return s.queue }

type ownershipFaultQueue struct {
	work.WorkQueue
	stage, reservedKey string
}

func (q *ownershipFaultQueue) Enqueue(ctx context.Context, kind, key string, payload []byte, at time.Time) error {
	err := q.WorkQueue.Enqueue(ctx, kind, key, payload, at)
	if kind == replication.TargetWorkKind {
		q.reservedKey = key
		if err == nil && q.stage == "enqueue" {
			return errExecutionStorage
		}
	}
	return err
}

func (q *ownershipFaultQueue) LeaseKey(ctx context.Context, kind, key string, lease work.WorkLease) (work.WorkItem, bool, error) {
	item, taken, err := q.WorkQueue.LeaseKey(ctx, kind, key, lease)
	if err == nil && taken && kind == replication.TargetWorkKind && q.stage == "lease" {
		return item, taken, errExecutionStorage
	}
	return item, taken, err
}

func (q *ownershipFaultQueue) Extend(ctx context.Context, kind, key string, generation int64, until, now time.Time) error {
	if err := q.WorkQueue.Extend(ctx, kind, key, generation, until, now); err != nil {
		return err
	}
	if kind == replication.TargetWorkKind && q.stage == "extend" {
		return errExecutionStorage
	}
	return nil
}

func (q *ownershipFaultQueue) Release(ctx context.Context, kind, key string, generation int64, now time.Time) error {
	if kind == replication.TargetWorkKind && q.stage == "release" {
		return errExecutionStorage
	}
	return q.WorkQueue.Release(ctx, kind, key, generation, now)
}

func TestUnitReplicationOwnershipAdmissionStorageFailureRollsBackReservation(t *testing.T) {
	for _, stage := range []string{"enqueue", "lease"} {
		t.Run(stage, func(t *testing.T) {
			db, ownership, _, items := ownershipFixture(t)
			queue := &ownershipFaultQueue{WorkQueue: db.WorkQueue(), stage: stage}
			ownership.Store = ownershipFaultStore{OwnershipStore: db, queue: queue}
			entered := false
			err := ownership.Run(t.Context(), items[0], "target", func(context.Context, replication.Owner) error { entered = true; return nil })
			if !errors.Is(err, errExecutionStorage) || entered || queue.reservedKey == "" {
				t.Fatal("failed target reservation entered transfer or missed the injected write", err)
			}
			if _, err := db.WorkQueue().Get(t.Context(), replication.TargetWorkKind, queue.reservedKey); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("failed target admission retained a reservation", err)
			}
			current, err := db.WorkQueue().Get(t.Context(), items[0].Kind, items[0].Key)
			must(t, err)
			if !reflect.DeepEqual(current, items[0]) {
				t.Fatal("failed target admission changed the run lease")
			}
			queue.stage = ""
			owner, err := ownership.Claim(t.Context(), items[1], "target")
			must(t, err)
			must(t, ownership.Release(t.Context(), owner))
		})
	}
}

func TestUnitReplicationOwnershipHeartbeatStorageOrGuardFailureCancelsTransfer(t *testing.T) {
	for _, stage := range []string{"guard", "extend"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				db, ownership, _, items := ownershipFixture(t)
				ownership.Duration = 30 * time.Millisecond
				queue := &ownershipFaultQueue{WorkQueue: db.WorkQueue(), stage: stage}
				ownership.Store = ownershipFaultStore{OwnershipStore: db, queue: queue}
				var checks atomic.Int32
				originalGuard := ownership.Guard
				ownership.Guard = func(ctx context.Context) error {
					if stage == "guard" && checks.Add(1) > 1 {
						return kernel.ErrForbidden
					}
					return originalGuard(ctx)
				}
				var retained replication.Owner
				err := ownership.Run(t.Context(), items[0], "target", func(ctx context.Context, owner replication.Owner) error {
					retained = owner
					select {
					case <-ctx.Done():
					case <-time.After(time.Second):
						t.Fatal("heartbeat refusal did not cancel actual transfer work")
					}
					return nil // A benign callback return cannot hide failed ownership.
				})
				var want error = errExecutionStorage
				if stage == "guard" {
					want = kernel.ErrForbidden
				}
				if !errors.Is(err, want) {
					t.Fatal("heartbeat refusal became false transfer success", err)
				}
				current, err := db.WorkQueue().Get(t.Context(), retained.Target.Kind, retained.Target.Key)
				must(t, err)
				if current.State != work.WorkPending || current.Generation != retained.Target.Generation {
					t.Fatal("failed heartbeat stranded or replaced its reservation")
				}
				ownership.Guard, queue.stage = originalGuard, ""
				other, err := ownership.Claim(t.Context(), items[1], "target")
				must(t, err)
				must(t, ownership.Release(t.Context(), other))
			})
		})
	}
}

func TestUnitReplicationOwnershipCleanupFailureCannotHideOrReleaseAnotherRun(t *testing.T) {
	db, ownership, _, items := ownershipFixture(t)
	queue := &ownershipFaultQueue{WorkQueue: db.WorkQueue(), stage: "release"}
	ownership.Store = ownershipFaultStore{OwnershipStore: db, queue: queue}
	var retained replication.Owner
	err := ownership.Run(t.Context(), items[0], "target", func(_ context.Context, owner replication.Owner) error { retained = owner; return nil })
	if !errors.Is(err, errExecutionStorage) {
		t.Fatal("reservation cleanup failure became successful dispatch", err)
	}
	current, err := db.WorkQueue().Get(t.Context(), retained.Target.Kind, retained.Target.Key)
	must(t, err)
	if current.State != work.WorkLeased || current.Generation != retained.Target.Generation {
		t.Fatal("failed cleanup discarded the exact retained reservation")
	}
	if _, err := ownership.Claim(t.Context(), items[1], "target"); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("a competitor entered an unreleased reservation", err)
	}
	queue.stage = ""
	must(t, ownership.Release(t.Context(), retained))
	other, err := ownership.Claim(t.Context(), items[1], "target")
	must(t, err)
	must(t, ownership.Release(t.Context(), other))
}

func TestUnitReplicationOwnershipCancellationAfterQueueLocksRollsBackPublication(t *testing.T) {
	db, ownership, _, items := ownershipFixture(t)
	owner, err := ownership.Claim(t.Context(), items[0], "target")
	must(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ownership.Store = advancingOwnershipStore{OwnershipStore: db, advance: cancel}
	err = db.Tx(ctx, func(ctx context.Context) error {
		if err := db.Settings().Put(ctx, "cancelled-after-lease-locks", true); err != nil {
			return err
		}
		return ownership.Fence(ctx, owner)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation after queue locks retained publication authority", err)
	}
	var published bool
	if err := db.Settings().Get(t.Context(), "cancelled-after-lease-locks", &published); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("cancelled ownership fence retained publication", err)
	}
	ownership.Store = db
	must(t, db.Tx(t.Context(), func(ctx context.Context) error { return ownership.Fence(ctx, owner) }))
	must(t, ownership.Release(t.Context(), owner))
}
