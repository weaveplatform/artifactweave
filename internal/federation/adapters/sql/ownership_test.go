package sql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platformsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestAccReplicationOwnershipSQLTakeoverCannotPublishOrReleaseAFormerGeneration(t *testing.T) {
	sqltest.RunModule(
		t,
		[]dialect.Module{platformsql.Module},
		func(db *dialect.Store) *platformsql.Store { return platformsql.New(db) },
		func(t *testing.T, newStore func(*testing.T) *platformsql.Store) {
			db := newStore(t)
			now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
			ownership := replication.Ownership{
				Store: db, Now: func() time.Time { return now }, Duration: 10 * time.Second,
				Guard: func(ctx context.Context) error {
					if !db.InTx(ctx) {
						return kernel.ErrInvalid
					}
					return nil
				},
			}
			for _, id := range []string{"manual", "scheduled"} {
				if err := db.WorkQueue().Enqueue(t.Context(), "replication:run", id, nil, now); err != nil {
					t.Fatal(err)
				}
			}
			items, err := db.WorkQueue().Lease(t.Context(), work.WorkLease{Kinds: []string{"replication:run"}, Holder: "worker-a", Limit: 2, Now: now, Until: now.Add(time.Minute)})
			if err != nil || len(items) != 2 {
				t.Fatal("logical run attempts", err)
			}
			old, err := ownership.Claim(t.Context(), items[0], "target-lifetime")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ownership.Claim(t.Context(), items[1], "target-lifetime"); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("manual and scheduled work simultaneously claimed a target", err)
			}
			now = now.Add(11 * time.Second)
			if err = ownership.Renew(t.Context(), old); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("expired target reservation resurrected", err)
			}
			newer, err := ownership.Claim(t.Context(), items[1], "target-lifetime")
			if err != nil || newer.Target.Generation <= old.Target.Generation {
				t.Fatal("target takeover did not fence generation", err)
			}
			if err = ownership.Release(t.Context(), old); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("old generation released successor", err)
			}
			err = db.Tx(t.Context(), func(ctx context.Context) error {
				if err := db.Settings().Put(ctx, "replication-ownership-marker", true); err != nil {
					return err
				}
				return ownership.Fence(ctx, old)
			})
			if !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("stale worker published status", err)
			}
			var marker bool
			if err = db.Settings().Get(t.Context(), "replication-ownership-marker", &marker); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("ownership refusal did not roll back publication", err)
			}
			if err = ownership.Release(t.Context(), newer); err != nil {
				t.Fatal(err)
			}
			if err = db.Tx(t.Context(), func(ctx context.Context) error { return ownership.Fence(ctx, old) }); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("released queue row revived former generation", err)
			}
			retained, err := db.WorkQueue().Get(t.Context(), newer.Target.Kind, newer.Target.Key)
			if err != nil || retained.State != work.WorkPending || retained.Generation != newer.Target.Generation {
				t.Fatal("target reservation lost persistent generation history", err)
			}
		},
		sqltest.WithPostgres(),
	)
}
