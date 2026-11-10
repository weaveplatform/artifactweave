package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// replicationStore opens a SQLite store, sealed with the test keyring, that
// holds the federation module's tables alone and the replication k1 of
// repo-a, with a password and one run.
func replicationStore(t *testing.T) *fedsql.Store {
	t.Helper()
	ctx := context.Background()
	s := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
	for _, err := range []error{
		s.Replications().Put(ctx, model.Replication{Key: "k1", RepoKey: "repo-a", Password: "pw"}),
		s.Replications().
			PutRun(ctx, model.ReplicationRun{ReplicationKey: "k1", Started: time.Unix(1, 0)}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestAccReplicationsFaults injects a failure into the statement after a
// successful one and checks every multi-statement write reports it instead
// of half-applying or swallowing it.
func TestAccReplicationsFaults(t *testing.T) {
	cases := []struct {
		name   string
		inject []string
		op     func(context.Context, *fedsql.Store) error
	}{
		{
			"delete replication runs",
			[]string{sqltest.DropTable("replication_runs")},
			func(ctx context.Context, s *fedsql.Store) error { return s.Replications().Delete(ctx, "k1") },
		},
		{
			"delete replication",
			[]string{sqltest.FailTrigger("DELETE", "replications")},
			func(ctx context.Context, s *fedsql.Store) error { return s.Replications().Delete(ctx, "k1") },
		},
		{
			"delete repo replication runs",
			[]string{sqltest.DropTable("replication_runs")},
			func(ctx context.Context, s *fedsql.Store) error {
				return s.Replications().DeleteForRepo(ctx, "repo-a")
			},
		},
		{
			"delete repo replications",
			[]string{sqltest.FailTrigger("DELETE", "replications")},
			func(ctx context.Context, s *fedsql.Store) error {
				return s.Replications().DeleteForRepo(ctx, "repo-a")
			},
		},
		{
			"put replication run",
			[]string{sqltest.FailTrigger("INSERT", "replication_runs")},
			func(ctx context.Context, s *fedsql.Store) error {
				return s.Replications().
					PutRun(ctx, model.ReplicationRun{ReplicationKey: "k1", Started: time.Unix(9, 0)})
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := replicationStore(t)
			sqltest.Exec(t, s.DB(), c.inject...)
			if err := c.op(ctx, s); err == nil || errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestAccReplicationsSealed reads the sealed replication password through a
// second store over the same database without the keyring that sealed it:
// the read must refuse rather than return ciphertext.
func TestAccReplicationsSealed(t *testing.T) {
	ctx := context.Background()
	sealed := replicationStore(t)
	s := fedsql.New(dialect.New(sealed.DB(), sealed.Dialect(), nil))
	for name, op := range map[string]func() error{
		"replication": func() error { _, err := s.Replications().Get(ctx, "k1"); return err },
	} {
		if err := op(); !errors.Is(err, dialect.ErrNoKeyring) {
			t.Errorf("%s: want ErrNoKeyring, got %v", name, err)
		}
	}
	// Without a keyring a new value is stored in plain text and reads back.
	if err := s.Replications().
		Put(ctx, model.Replication{Key: "plain", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Replications().Get(ctx, "plain"); err != nil || got.Password != "pw" {
		t.Fatalf("plain replication = %+v, %v", got, err)
	}
}

// TestAccReplicationsUnmigrated runs every replication method against a
// database without the schema: each must surface the driver error, never a
// nil error or a misleading ErrNotFound.
func TestAccReplicationsUnmigrated(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenDB(filepath.Join(t.TempDir(), "raw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rs := fedsql.New(dialect.New(db, sqlite.Dialect, nil)).Replications()
	now := time.Unix(100, 0)
	ops := map[string]error{
		"put replication":    rs.Put(ctx, model.Replication{Key: "k"}),
		"get replication":    sqltest.Err(rs.Get(ctx, "k")),
		"repo replications":  sqltest.Err(rs.ForRepo(ctx, "r")),
		"list replications":  sqltest.Err(rs.List(ctx)),
		"delete replication": rs.Delete(ctx, "k"),
		"delete for repo":    rs.DeleteForRepo(ctx, "r"),
		"put replication run": rs.PutRun(
			ctx,
			model.ReplicationRun{ReplicationKey: "k", Started: now},
		),
		"last replication": sqltest.Err(rs.LastRun(ctx, "k")),
	}
	for name, err := range ops {
		if err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
