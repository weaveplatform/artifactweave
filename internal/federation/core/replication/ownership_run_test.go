package replication_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestUnitReplicationOwnershipRunRenewsAndJoinsBeforeReleasing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, ownership, _, items := ownershipFixture(t)
		ownership.Duration = 90 * time.Millisecond
		var guards atomic.Int32
		renewed := make(chan struct{}, 1)
		ownership.Guard = func(context.Context) error {
			if guards.Add(1) > 1 {
				select {
				case renewed <- struct{}{}:
				default:
				}
			}
			return nil
		}
		var retained replication.Owner
		must(t, ownership.Run(t.Context(), items[0], "target", func(ctx context.Context, owner replication.Owner) error {
			retained = owner
			if db.InTx(ctx) {
				t.Fatal("network callback borrowed a metadata transaction")
			}
			select {
			case <-renewed:
			case <-time.After(5 * time.Second):
				t.Fatal("target heartbeat did not check current ownership")
			}
			if _, err := ownership.Claim(ctx, items[1], "target"); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("a manual/scheduled competitor entered a running transfer", err)
			}
			return db.Tx(ctx, func(ctx context.Context) error { return ownership.Fence(ctx, owner) })
		}))
		current, err := db.WorkQueue().Get(t.Context(), retained.Target.Kind, retained.Target.Key)
		must(t, err)
		if current.State != work.WorkPending || current.Generation != retained.Target.Generation {
			t.Fatal("completed callback did not release its exact reservation")
		}
		other, err := ownership.Claim(t.Context(), items[1], "target")
		must(t, err)
		must(t, ownership.Release(t.Context(), other))
	})
}

func TestUnitReplicationOwnershipRunCancelsTransferOnLostRunAndRefusesFalseSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, ownership, _, items := ownershipFixture(t)
		ownership.Duration = 30 * time.Millisecond
		err := ownership.Run(t.Context(), items[0], "target", func(ctx context.Context, owner replication.Owner) error {
			must(t, db.WorkQueue().Release(ctx, owner.Run.Kind, owner.Run.Key, owner.Run.Generation, ownership.Now()))
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("lost queue ownership did not cancel the active transfer")
			}
			return nil
		})
		if !errors.Is(err, kernel.ErrConflict) {
			t.Fatal("callback nil hid lost execution ownership", err)
		}
		other, err := ownership.Claim(t.Context(), items[1], "target")
		must(t, err)
		must(t, ownership.Release(t.Context(), other))
	})
}

func TestUnitReplicationOwnershipRunCancellationAndPanicReleaseTheReservation(t *testing.T) {
	for _, mode := range []string{"cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			db, ownership, _, items := ownershipFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var retained replication.Owner
			func() {
				defer func() {
					if recovered := recover(); mode == "panic" && recovered != "test-panic" {
						t.Fatal("transfer panic was swallowed or changed")
					}
				}()
				err := ownership.Run(ctx, items[0], "target", func(_ context.Context, owner replication.Owner) error {
					retained = owner
					if mode == "panic" {
						panic("test-panic")
					}
					cancel()
					return nil
				})
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("shutdown reported successful execution", err)
				}
			}()
			current, err := db.WorkQueue().Get(t.Context(), retained.Target.Kind, retained.Target.Key)
			must(t, err)
			if current.State != work.WorkPending || current.Generation != retained.Target.Generation {
				t.Fatal("shutdown/panic stranded the target reservation")
			}
		})
	}
}
