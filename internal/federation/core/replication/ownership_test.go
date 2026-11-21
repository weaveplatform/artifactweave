package replication_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func ownershipFixture(t *testing.T) (*inmem.Store, replication.Ownership, *time.Time, []work.WorkItem) {
	t.Helper()
	db := inmem.New()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	owner := replication.Ownership{
		Store: db, Now: func() time.Time { return now }, Duration: 10 * time.Second,
		Guard: func(ctx context.Context) error {
			if !db.InTx(ctx) {
				return kernel.ErrInvalid
			}
			return nil
		},
	}
	for _, key := range []string{"manual", "scheduled"} {
		must(t, db.WorkQueue().Enqueue(t.Context(), "replication:run", key, nil, now))
	}
	items, err := db.WorkQueue().Lease(t.Context(), work.WorkLease{
		Kinds: []string{"replication:run"}, Holder: "worker", Limit: 2, Now: now, Until: now.Add(time.Minute),
	})
	must(t, err)
	if len(items) != 2 {
		t.Fatal("run attempts were not leased")
	}
	return db, owner, &now, items
}

func TestUnitReplicationOwnershipSerializesManualAndScheduledRunsWithoutCoalescing(t *testing.T) {
	db, ownership, _, items := ownershipFixture(t)
	first, err := ownership.Claim(t.Context(), items[0], "target-lifetime")
	must(t, err)
	if _, err = ownership.Claim(t.Context(), items[1], "target-lifetime"); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("simultaneous manual/scheduled runs owned one target", err)
	}
	other, err := ownership.Claim(t.Context(), items[1], "another-target")
	must(t, err)
	must(t, ownership.Release(t.Context(), other))
	must(t, db.Tx(t.Context(), func(ctx context.Context) error { return ownership.Fence(ctx, first) }))
	for _, item := range items {
		current, err := db.WorkQueue().Get(t.Context(), item.Kind, item.Key)
		must(t, err)
		if current.Generation != item.Generation || current.State != work.WorkLeased || current.Key != item.Key {
			t.Fatal("target coordination replaced a distinct logical run")
		}
	}
}

func TestUnitReplicationOwnershipExpiredReservationCannotResurrectOrReleaseItsSuccessor(t *testing.T) {
	db, ownership, now, items := ownershipFixture(t)
	old, err := ownership.Claim(t.Context(), items[0], "same-target")
	must(t, err)
	*now = now.Add(11 * time.Second)
	if err = ownership.Renew(t.Context(), old); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("expired reservation was resurrected", err)
	}
	newer, err := ownership.Claim(t.Context(), items[1], "same-target")
	must(t, err)
	if newer.Target.Generation <= old.Target.Generation {
		t.Fatal("target ownership generation did not advance")
	}
	if err = ownership.Release(t.Context(), old); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old owner released a successor", err)
	}
	if err = db.Tx(t.Context(), func(ctx context.Context) error { return ownership.Fence(ctx, old) }); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old owner published after takeover", err)
	}
	must(t, ownership.Release(t.Context(), newer))
	if err = db.Tx(t.Context(), func(ctx context.Context) error { return ownership.Fence(ctx, old) }); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old generation revived after successor release", err)
	}
	retained, err := db.WorkQueue().Get(t.Context(), newer.Target.Kind, newer.Target.Key)
	must(t, err)
	if retained.State != work.WorkPending || retained.Generation != newer.Target.Generation {
		t.Fatal("release discarded generation history")
	}
}

func TestUnitReplicationOwnershipLostRunLeaseRollsBackPublication(t *testing.T) {
	for _, mode := range []string{"expired", "taken-over"} {
		t.Run(mode, func(t *testing.T) {
			db, ownership, now, items := ownershipFixture(t)
			ownership.Duration = 2 * time.Minute
			owner, err := ownership.Claim(t.Context(), items[0], "target")
			must(t, err)
			*now = now.Add(time.Minute + time.Second)
			if mode == "taken-over" {
				_, taken, err := db.WorkQueue().LeaseKey(t.Context(), owner.Run.Kind, owner.Run.Key, work.WorkLease{Holder: "successor", Now: *now, Until: now.Add(time.Minute)})
				must(t, err)
				if !taken {
					t.Fatal("run takeover did not occur")
				}
			}
			err = db.Tx(t.Context(), func(ctx context.Context) error {
				if err := db.Settings().Put(ctx, "replication-test-publication", true); err != nil {
					return err
				}
				return ownership.Fence(ctx, owner)
			})
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("expired run published state", err)
			}
			var published bool
			if err = db.Settings().Get(t.Context(), "replication-test-publication", &published); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("lost ownership did not roll back domain state", err)
			}
		})
	}
}

type advancingOwnershipStore struct {
	replication.OwnershipStore
	advance func()
}

func (s advancingOwnershipStore) WorkQueue() work.WorkQueue {
	return advancingOwnershipQueue{WorkQueue: s.OwnershipStore.WorkQueue(), advance: s.advance}
}

type advancingOwnershipQueue struct {
	work.WorkQueue
	advance func()
}

func (q advancingOwnershipQueue) GuardLease(ctx context.Context, kind, key string, generation int64) (time.Time, error) {
	until, err := q.WorkQueue.GuardLease(ctx, kind, key, generation)
	if err == nil && kind == "replication:run" {
		q.advance()
	}
	return until, err
}

func TestUnitReplicationOwnershipReadsFreshClockAfterBothLocksAndRenewalPreservesGeneration(t *testing.T) {
	db, ownership, now, items := ownershipFixture(t)
	owner, err := ownership.Claim(t.Context(), items[0], "target")
	must(t, err)
	*now = now.Add(5 * time.Second)
	must(t, ownership.Renew(t.Context(), owner))
	current, err := db.WorkQueue().Get(t.Context(), owner.Target.Kind, owner.Target.Key)
	must(t, err)
	if current.Generation != owner.Target.Generation || !current.LeaseUntil.Equal(now.Add(10*time.Second)) {
		t.Fatal("renewal replaced ownership or failed to extend it")
	}
	ownership.Store = advancingOwnershipStore{OwnershipStore: db, advance: func() { *now = now.Add(time.Minute) }}
	if err = db.Tx(t.Context(), func(ctx context.Context) error { return ownership.Fence(ctx, owner) }); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("lock wait borrowed an earlier unexpired clock", err)
	}
	if err = ownership.Fence(t.Context(), owner); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("publication fence accepted a nontransactional context", err)
	}
}
