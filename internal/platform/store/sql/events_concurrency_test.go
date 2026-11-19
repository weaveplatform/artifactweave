package sql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func eventFixture() outbox.Event {
	return outbox.Event{Origin: "instance", Topic: "artifact.deployed", Key: "libs/a.jar", At: time.Now().UTC()}
}

func TestAccModuleEventReadDetectsMissingPrefix(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newStore func(*testing.T) *dialect.Store) {
			db, ctx := newStore(t), t.Context()
			s := platsql.New(db)
			storekit.Get[outbox.Position](t)(s.Events().RegisterConsumer(ctx, "instance", "reader"))
			for range 2 {
				storekit.Get[outbox.Event](t)(s.Events().AppendEvent(ctx, eventFixture()))
			}
			// Model the cursor/batch snapshot gap that a concurrent advancing
			// worker can expose under READ COMMITTED. No effects may be applied
			// from the incomplete prefix; the caller must reread its position.
			sqltest.Exec(t, db.DB(), "DELETE FROM outbox WHERE seq = 1")
			if _, _, err := s.Events().Next(ctx, "instance", "reader", 10); !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("missing contiguous prefix: %v", err)
			}
		}, sqltest.WithPostgres())
}

func TestAccModuleEventReadDoesNotInvertProducerLocks(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newStore func(*testing.T) *dialect.Store) {
			db := newStore(t)
			if db.Dialect().Name != "postgres" {
				// SQLite serializes whole writer transactions; the shared contract
				// covers that path. Row-lock inversion is PostgreSQL-specific.
				return
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			s := platsql.New(db)
			e := s.Events()
			storekit.Get[outbox.Position](t)(e.RegisterConsumer(ctx, "instance", "reader"))
			storekit.Get[outbox.Event](t)(e.AppendEvent(ctx, eventFixture()))
			domainLocked, consumerRead := make(chan struct{}), make(chan struct{})
			errs := make(chan error, 2)
			writeDomain := func(ctx context.Context) error {
				_, err := s.Outbox().Append(ctx, outbox.OutboxEntry{Origin: "instance", Stream: "domain-lock"})
				return err
			}
			go func() {
				errs <- s.Tx(ctx, func(ctx context.Context) error {
					if err := writeDomain(ctx); err != nil {
						close(domainLocked)
						return err
					}
					close(domainLocked)
					select {
					case <-consumerRead:
					case <-ctx.Done():
						return ctx.Err()
					}
					_, err := e.AppendEvent(ctx, eventFixture())
					return err
				})
			}()
			go func() {
				<-domainLocked
				errs <- s.Tx(ctx, func(ctx context.Context) error {
					batch, p, err := e.Next(ctx, "instance", "reader", 1)
					close(consumerRead)
					if err != nil {
						return err
					}
					if len(batch) != 1 {
						return kernel.ErrNotFound
					}
					if err := writeDomain(ctx); err != nil {
						return err
					}
					return e.Ack(ctx, p, batch[0].Seq)
				})
			}()
			first, second := <-errs, <-errs
			storekit.Must(t, first)
			storekit.Must(t, second)
			batch, p, err := e.Next(ctx, "instance", "reader", 10)
			storekit.Must(t, err)
			if p.Seq != 1 || len(batch) != 1 || batch[0].Seq != 2 {
				t.Fatalf("producer/consumer progress: %+v at %+v", batch, p)
			}
		}, sqltest.WithPostgres())
}
