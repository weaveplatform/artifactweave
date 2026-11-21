package sql_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccReplicationScheduleAuthorUpgradePreservesLegacyConfiguration(t *testing.T) {
	baseline := fedsql.Module
	baseline.AfterMigrate = nil
	files := fstest.MapFS{}
	for _, name := range []string{
		"sqlite/0001_baseline.sql", "postgres/0001_baseline.sql",
		"sqlite/0002_replication_revision.sql", "postgres/0002_replication_revision.sql",
		"sqlite/0003_push_progress.sql", "postgres/0003_push_progress.sql",
	} {
		data, err := migrations.FS.ReadFile(name)
		storekit.Must(t, err)
		files[name] = &fstest.MapFile{Data: data}
	}
	baseline.Migrations = files
	sqltest.RunModule(t, []dialect.Module{baseline}, func(db *dialect.Store) *dialect.Store { return db }, func(t *testing.T, newDB func(*testing.T) *dialect.Store) {
		db, ctx := newDB(t), t.Context()
		password, err := db.Seal("replications.password", "scheduled", "private-fixture-value")
		storekit.Must(t, err)
		_, err = db.Exec(ctx, "seed scheduled legacy target",
			"INSERT INTO replications (replication_key,repo_key,url,password,enabled,cron_exp,private_revision) VALUES (?,?,?,?,?,?,?)",
			"scheduled", "remote", "https://peer.example/artifactory/source", password, true, "0 * * * * ?", "legacy-revision")
		storekit.Must(t, err)
		_, err = db.Exec(ctx, "seed legacy completion",
			"INSERT INTO replication_runs (replication_key,started_at,completed_at,status) VALUES (?,?,?,?)",
			"scheduled", time.Unix(10, 0).UnixMicro(), time.Unix(20, 0).UnixMicro(), "partial_failure")
		storekit.Must(t, err)
		_, err = dialect.Migrate(ctx, db.DB(), db.Dialect(), []dialect.Module{fedsql.Module}, "federation")
		storekit.Must(t, err)
		storekit.Must(t, fedsql.Module.AfterMigrate(ctx, db))
		store := fedsql.New(db)
		upgraded, err := store.Replications().Get(ctx, "scheduled")
		storekit.Must(t, err)
		if upgraded.Key != "scheduled" || upgraded.RepoKey != "remote" || upgraded.Revision != "legacy-revision" || upgraded.CronExp != "0 * * * * ?" || !upgraded.Enabled ||
			upgraded.Password != "private-fixture-value" ||
			upgraded.ScheduleAuthor != (kernel.CredentialReference{}) {
			t.Fatal("upgrade changed legacy configuration or invented an authenticated schedule author")
		}
		if !bytes.Equal(replicationCiphertext(t, store, "scheduled"), password) {
			t.Fatal("schedule author upgrade rewrote the sealed target credential")
		}
		run, err := store.Replications().LastRun(ctx, "scheduled")
		storekit.Must(t, err)
		if !run.Completed.Equal(time.Unix(20, 0)) || run.Status != "partial_failure" {
			t.Fatal("schedule author upgrade changed retained execution evidence")
		}
		upgraded.ScheduleAuthor = kernel.CredentialReference{Name: "verified-schedule-author", Password: true, PasswordRevision: [32]byte{7}}
		storekit.Must(t, store.Replications().Put(ctx, upgraded))
		reopened, err := fedsql.New(db).Replications().Get(ctx, "scheduled")
		storekit.Must(t, err)
		if reopened != upgraded {
			t.Fatal("private schedule reference did not survive reopening")
		}
		_, err = db.Exec(ctx, "corrupt private schedule metadata", "UPDATE replications SET schedule_author_json = ? WHERE replication_key = ?", "{", "scheduled")
		storekit.Must(t, err)
		if _, err = store.Replications().Get(ctx, "scheduled"); !errors.Is(err, kernel.ErrStore) {
			t.Fatal("malformed private reference was silently accepted", err)
		}
		// A status reader must neither unseal target credentials nor decode the
		// private schedule reference when projecting public history.
		reader := fedsql.New(dialect.New(db.DB(), db.Dialect(), nil))
		states, _, err := reader.Replications().StatusTargets(ctx, "remote", kernel.Page{})
		if err != nil || len(states) != 1 || states[0].Latest.Status != run.Status {
			t.Fatal("credential-free status became dependent on private author metadata", err)
		}
	}, sqltest.WithPostgres())
}

func TestAccReplicationScheduleAuthorContextCancellationPreservesTarget(t *testing.T) {
	s := replicationStore(t)
	before, err := s.Replications().Get(t.Context(), "k1")
	storekit.Must(t, err)
	changed := before
	changed.ScheduleAuthor = kernel.CredentialReference{Name: "caller", TokenID: "reference-only"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = s.Replications().Put(ctx, changed); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled private metadata write did not report cancellation", err)
	}
	after, err := s.Replications().Get(t.Context(), "k1")
	storekit.Must(t, err)
	if after != before {
		t.Fatal("cancelled write changed private schedule authority")
	}
}
