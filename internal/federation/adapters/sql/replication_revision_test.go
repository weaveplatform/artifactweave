package sql_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccReplicationRevisionUpgradePreservesSealedCredentials(t *testing.T) {
	baseline := fedsql.Module
	baseline.AfterMigrate = nil
	files := fstest.MapFS{}
	for _, name := range []string{"sqlite/0001_baseline.sql", "postgres/0001_baseline.sql"} {
		data, err := migrations.FS.ReadFile(name)
		storekit.Must(t, err)
		files[name] = &fstest.MapFile{Data: data}
	}
	baseline.Migrations = files
	sqltest.RunModule(t, []dialect.Module{baseline}, func(db *dialect.Store) *dialect.Store { return db }, func(t *testing.T, newDB func(*testing.T) *dialect.Store) {
		db, ctx := newDB(t), t.Context()
		// Cross the bounded backfill page and preserve every key, option and
		// sealed value. The old schema is seeded through its original columns.
		storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error {
			for i := range kernel.MaxLimit + 1 {
				key := fmt.Sprintf("legacy-%04d", i)
				password, err := db.Seal("replications.password", key, "fake-legacy-password")
				if err != nil {
					return err
				}
				if _, err := db.Exec(
					ctx,
					"seed old replication",
					"INSERT INTO replications (replication_key,repo_key,url,password,enabled) VALUES (?,?,?,?,?)",
					key,
					"local",
					"https://target.example/repo",
					password,
					true,
				); err != nil {
					return err
				}
			}
			return nil
		}))
		_, err := dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{fedsql.Module}, "federation")
		storekit.Must(t, err)
		storekit.Must(t, fedsql.Module.AfterMigrate(ctx, db))
		first := revisionRows(t, db)
		if len(first) != kernel.MaxLimit+1 {
			t.Fatalf("upgraded revisions = %d", len(first))
		}
		seen := map[string]bool{}
		for _, revision := range first {
			if revision == "" || seen[revision] {
				t.Fatal("upgrade did not assign distinct nonempty revisions")
			}
			seen[revision] = true
		}
		for range 2 {
			storekit.Must(t, fedsql.Module.AfterMigrate(ctx, db))
			storekit.Same(t, "repeated upgrade keeps authority", first, revisionRows(t, db))
		}
		rows, err := fedsql.New(db).Replications().List(ctx)
		storekit.Must(t, err)
		for _, r := range rows {
			if r.Password != "fake-legacy-password" || r.URL != "https://target.example/repo" || !r.Enabled || r.Revision != first[r.Key] {
				t.Fatal("revision upgrade changed legacy target configuration or sealed credentials")
			}
		}
	}, sqltest.WithPostgres())
}

func revisionRows(t *testing.T, db *dialect.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	storekit.Must(
		t,
		db.Rows(t.Context(), "read replication revisions", "SELECT replication_key,private_revision FROM replications ORDER BY replication_key", nil, func(row *sql.Rows) error {
			var key, revision string
			if err := row.Scan(&key, &revision); err != nil {
				return err
			}
			out[key] = revision
			return nil
		}),
	)
	return out
}
