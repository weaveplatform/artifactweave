package sql_test

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func failEventWrite(t *testing.T, db *dialect.Store, operation, table string) {
	t.Helper()
	if db.Dialect().Name == "postgres" {
		sqltest.Exec(t, db.DB(),
			"CREATE FUNCTION event_write_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$",
			"CREATE TRIGGER event_write_failure BEFORE "+operation+" ON "+table+" FOR EACH ROW EXECUTE FUNCTION event_write_failure()")
		return
	}
	sqltest.Exec(t, db.DB(), sqltest.FailTrigger(operation, table))
}

func TestAccModuleEventWriteFaultsRollBack(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newStore func(*testing.T) *dialect.Store) {
			t.Run("Append", func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				s := platsql.New(db)
				failEventWrite(t, db, "INSERT", "outbox")
				if _, err := s.Events().AppendEvent(ctx, eventFixture()); err == nil {
					t.Fatal("append ignored an injected write failure")
				}
				if seq := storekit.Get[int64](t)(s.Outbox().LastSeq(ctx, "instance", outbox.ModuleEventStream)); seq != 0 {
					t.Fatalf("failed event kept its allocated sequence: %d", seq)
				}
			})
			t.Run("Register", func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				s := platsql.New(db)
				failEventWrite(t, db, "INSERT", "outbox_consumers")
				if _, err := s.Events().RegisterConsumer(ctx, "instance", "reader"); err == nil {
					t.Fatal("registration ignored an injected write failure")
				}
				if streams := storekit.Get[[]string](t)(s.Outbox().Streams(ctx, "instance")); len(streams) != 0 {
					t.Fatalf("failed registration left a sequence row: %v", streams)
				}
			})
			t.Run("AckAndEffect", func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				s := platsql.New(db)
				p := storekit.Get[outbox.Position](t)(s.Events().RegisterConsumer(ctx, "instance", "reader"))
				storekit.Get[outbox.Event](t)(s.Events().AppendEvent(ctx, eventFixture()))
				failEventWrite(t, db, "UPDATE", "outbox_consumers")
				err := s.Tx(ctx, func(ctx context.Context) error {
					if _, err := s.Outbox().Append(ctx, outbox.OutboxEntry{Origin: "instance", Stream: "effect"}); err != nil {
						return err
					}
					return s.Events().Ack(ctx, p, 1)
				})
				if err == nil {
					t.Fatal("acknowledgement ignored an injected write failure")
				}
				if seq := storekit.Get[int64](t)(s.Outbox().LastSeq(ctx, "instance", "effect")); seq != 0 {
					t.Fatalf("failed acknowledgement committed an effect: %d", seq)
				}
				batch, got, err := s.Events().Next(ctx, "instance", "reader", 1)
				storekit.Must(t, err)
				if got != p || len(batch) != 1 {
					t.Fatalf("failed acknowledgement advanced the consumer: %+v, %+v", batch, got)
				}
			})
			t.Run("PruneFloor", func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				s := platsql.New(db)
				p := storekit.Get[outbox.Position](t)(s.Events().RegisterConsumer(ctx, "instance", "reader"))
				storekit.Get[outbox.Event](t)(s.Events().AppendEvent(ctx, eventFixture()))
				storekit.Must(t, s.Events().Ack(ctx, p, 1))
				failEventWrite(t, db, "UPDATE", "outbox_sequences")
				if n, err := s.Events().PruneAcknowledged(ctx, "instance", 1); n != 0 || err == nil {
					t.Fatalf("failed prune reported success: removed=%d err=%v", n, err)
				}
				entries := storekit.Get[[]outbox.OutboxEntry](t)(s.Outbox().Read(ctx, "instance", outbox.ModuleEventStream, 0, 10))
				if len(entries) != 1 {
					t.Fatalf("prune deletion survived floor-update failure: %+v", entries)
				}
				late := storekit.Get[outbox.Position](t)(s.Events().RegisterConsumer(ctx, "instance", "late"))
				if late.Seq != 0 {
					t.Fatalf("failed prune advanced retained floor: %+v", late)
				}
			})
		}, sqltest.WithPostgres())
}
