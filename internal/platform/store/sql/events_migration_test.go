package sql_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/store/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccModuleEventMigrationPreservesPeerStreams(t *testing.T) {
	baseline := platsql.Module
	files := fstest.MapFS{}
	for _, backend := range []string{"sqlite", "postgres"} {
		name := backend + "/0001_baseline.sql"
		files[name] = &fstest.MapFile{Data: storekit.Get[[]byte](t)(fs.ReadFile(migrations.FS, name))}
	}
	baseline.Migrations = files
	sqltest.RunModule(t, []dialect.Module{baseline}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newStore func(*testing.T) *dialect.Store) {
			db, ctx := newStore(t), t.Context()
			st := platsql.New(db)
			at := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)
			before := storekit.Get[outbox.OutboxEntry](t)(st.Outbox().Append(ctx, outbox.OutboxEntry{
				Origin: "legacy-origin", Stream: "repo:libs", EventType: "deployed", Created: at,
				Time: outbox.HLC{Wall: at.UnixMilli(), Logical: 42}, Payload: []byte(`{"path":"a.jar"}`),
			}))
			cursor := outbox.PeerCursor{PeerInstanceID: "peer-one", Stream: "repo:libs", AckedSeq: before.Seq, LastSent: at}
			storekit.Must(t, st.Outbox().PutCursor(ctx, cursor))
			marker := outbox.AppliedMarker{Origin: "peer-one", Stream: "repo:libs", LastSeq: 7}
			storekit.Must(t, st.Outbox().SetApplied(ctx, marker))
			// Fail after the additive DDL. Neither a half-applied schema nor a
			// history row claiming success may survive the failed migration.
			broken := platsql.Module
			brokenFiles := fstest.MapFS{}
			for _, backend := range []string{"sqlite", "postgres"} {
				for _, name := range []string{"0001_baseline.sql", "0002_module_event_consumers.sql"} {
					path := backend + "/" + name
					data := string(storekit.Get[[]byte](t)(fs.ReadFile(migrations.FS, path)))
					if strings.HasPrefix(name, "0002") {
						data = strings.Replace(data, "-- +down", "INSERT INTO missing_event_migration_table VALUES (1);\n-- +down", 1)
					}
					brokenFiles[path] = &fstest.MapFile{Data: []byte(data)}
				}
			}
			broken.Migrations = brokenFiles
			if _, err := dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{broken}, "platform"); !errors.Is(err, dialect.ErrMigration) {
				t.Fatalf("injected migration failure: %v", err)
			}
			versions := storekit.Get[[]dialect.StreamVersion](t)(dialect.Version(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}))
			if len(versions) != 1 || versions[0].Current != 1 {
				t.Fatalf("failed migration advanced history: %+v", versions)
			}
			for range 2 {
				_, err := dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}, "platform")
				storekit.Must(t, err)
				storekit.Same(t, "legacy event after upgrade", []outbox.OutboxEntry{before},
					storekit.Get[[]outbox.OutboxEntry](t)(st.Outbox().Read(ctx, "legacy-origin", "repo:libs", 0, 0)))
				storekit.Same(t, "peer cursor after upgrade", cursor,
					storekit.Get[outbox.PeerCursor](t)(st.Outbox().Cursor(ctx, "peer-one", "repo:libs")))
				storekit.Same(t, "peer applied marker after upgrade", marker,
					storekit.Get[outbox.AppliedMarker](t)(st.Outbox().Applied(ctx, "peer-one", "repo:libs")))
			}
			// Rolling back the additive, not-yet-used consumer schema preserves
			// the old application's stream/HLC/cursor data and next sequence.
			_, err := dialect.Rollback(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}, "platform", 1)
			storekit.Must(t, err)
			storekit.Same(t, "legacy event after rollback", []outbox.OutboxEntry{before},
				storekit.Get[[]outbox.OutboxEntry](t)(st.Outbox().Read(ctx, "legacy-origin", "repo:libs", 0, 0)))
			next := before
			next.Seq = 0
			if got := storekit.Get[outbox.OutboxEntry](t)(st.Outbox().Append(ctx, next)); got.Seq != 2 {
				t.Fatalf("rollback reset peer sequence: %d", got.Seq)
			}
			_, err = dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}, "platform")
			storekit.Must(t, err)
			p := storekit.Get[outbox.Position](t)(st.Events().RegisterConsumer(ctx, "legacy-origin", "new-consumer"))
			if p.Seq != 0 {
				t.Fatalf("new event consumer inherited a peer acknowledgement: %+v", p)
			}
			_, _, err = st.Events().Next(ctx, "legacy-origin", "new-consumer", 0)
			storekit.Must(t, err)
		}, sqltest.WithPostgres())
}
