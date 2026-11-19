package sql_test

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/store/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// Historical migration regressions pin their own head instead of assuming
// the latest migration is still the one whose rollback they exercise.
func moduleThrough(t *testing.T, head int) dialect.Module {
	t.Helper()
	module := platsql.Module
	files := fstest.MapFS{}
	for _, backend := range []string{"sqlite", "postgres"} {
		entries := storekit.Get[[]fs.DirEntry](t)(fs.ReadDir(migrations.FS, backend))
		for _, entry := range entries {
			var version int
			if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil || version > head {
				continue
			}
			path := backend + "/" + entry.Name()
			files[path] = &fstest.MapFile{Data: storekit.Get[[]byte](t)(fs.ReadFile(migrations.FS, path))}
		}
	}
	module.Migrations = files
	return module
}

func TestAccScheduleMigrationPreservesQueuedWorkAndEvents(t *testing.T) {
	baseline := moduleThrough(t, 2)
	sqltest.RunModule(t, []dialect.Module{baseline}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newStore func(*testing.T) *dialect.Store) {
			db, ctx := newStore(t), t.Context()
			st := platsql.New(db)
			now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
			storekit.Must(t, st.WorkQueue().Enqueue(ctx, "existing-maintenance", "key", nil, now))
			storekit.Get[outbox.Position](t)(st.Events().RegisterConsumer(ctx, "origin", "consumer"))
			broken := platsql.Module
			files := fstest.MapFS{}
			for _, backend := range []string{"sqlite", "postgres"} {
				entries := storekit.Get[[]fs.DirEntry](t)(fs.ReadDir(migrations.FS, backend))
				for _, entry := range entries {
					path := backend + "/" + entry.Name()
					data := string(storekit.Get[[]byte](t)(fs.ReadFile(migrations.FS, path)))
					if strings.HasPrefix(entry.Name(), "0003_") {
						data = strings.Replace(data, "-- +down", "INSERT INTO missing_schedule_migration_table VALUES (1);\n-- +down", 1)
					}
					files[path] = &fstest.MapFile{Data: []byte(data)}
				}
			}
			broken.Migrations = files
			_, err := dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{broken}, "platform")
			storekit.WantErr(t, err, dialect.ErrMigration)
			versions := storekit.Get[[]dialect.StreamVersion](t)(dialect.Version(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}))
			if len(versions) != 1 || versions[0].Current != 2 {
				t.Fatalf("failed DDL advanced history: %+v", versions)
			}
			_, err = dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}, "platform")
			storekit.Must(t, err)
			svc := &schedule.Service{Store: st, Now: func() time.Time { return now }}
			state := storekit.Get[schedule.State](t)(svc.Configure(ctx, schedule.Reference{Kind: "cleanup", Key: "policy"}, "0 * * * * ?", "", true))
			now = now.Add(time.Minute)
			if n := storekit.Get[int](t)(svc.Tick(ctx, 10)); n != 1 {
				t.Fatal("upgraded store did not schedule")
			}
			_, err = dialect.Rollback(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}, "platform", 2)
			storekit.Must(t, err)
			if _, err := st.WorkQueue().Get(ctx, "existing-maintenance", "key"); err != nil {
				t.Fatalf("schedule rollback lost interval work: %v", err)
			}
			if _, _, err := st.Events().Next(ctx, "origin", "consumer", 1); err != nil {
				t.Fatalf("schedule rollback lost consumer: %v", err)
			}
			_, err = dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{platsql.Module}, "platform")
			storekit.Must(t, err)
			_, err = st.Schedules().Get(ctx, state.Reference)
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("down/up retained removed schedule: %v", err)
			}
			fresh := storekit.Get[schedule.State](t)(svc.Configure(ctx, state.Reference, state.Cron, state.Zone, true))
			if fresh.Revision == state.Revision {
				t.Fatal("recreated schedule could admit an old queued occurrence")
			}
		}, sqltest.WithPostgres())
}
