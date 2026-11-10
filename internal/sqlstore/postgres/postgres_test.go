package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	pgdrv "github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/storetest"
)

func TestAccContract(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := postgres.Open(context.Background(), dsn,
			postgres.Options{Options: sqlcommon.Options{Keyring: k}, MaxOpenConns: 8})
		if err != nil {
			t.Fatal(err)
		}
		sqltest.Reset(t, s.DB(), pgdrv.Dialect)
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestAccOpenErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := postgres.Open(
		ctx,
		"",
		postgres.Options{},
	); !errors.Is(
		err,
		pgdrv.ErrDSNRequired,
	) {
		t.Fatalf("empty DSN: %v", err)
	}
	if _, err := postgres.Open(ctx, "postgres://%zz", postgres.Options{}); err == nil {
		t.Fatal("a malformed DSN parsed")
	}
	// Nothing listens on port 1, so the ping fails fast.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := postgres.Open(ctx, "postgres://aw@127.0.0.1:1/aw?connect_timeout=2",
		postgres.Options{ConnMaxLifetime: time.Minute}); err == nil {
		t.Fatal("connected to a closed port")
	}
}

func TestAccKeyRotation(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	first := true
	sqltest.Rotation(t, func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
		s, err := postgres.Open(
			context.Background(),
			dsn,
			postgres.Options{Options: sqlcommon.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			sqltest.Reset(t, s.DB(), pgdrv.Dialect)
			first = false
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// TestAccLegacyHistoryRefused opens a database whose history predates the
// per-module migration streams: the store and the migrator refuse it and
// write nothing. It runs in a scratch schema, so the refused history never
// meets the server's shared schema.
func TestAccLegacyHistoryRefused(t *testing.T) {
	ctx := t.Context()
	dsn := scratchSchema(t)
	sqltest.LegacyHistoryRefused(t, openUnmigrated(t, dsn).DB(), pgdrv.Dialect, func() error {
		refused, err := postgres.Open(ctx, dsn, postgres.Options{})
		if err == nil {
			_ = refused.Close()
		}
		return err
	})
}

// TestAccStreamsMigrateAlone migrates each module's stream up and down on its
// own in an empty scratch schema. A baseline that references a table of
// another stream, such as a foreign key across modules, fails here, because
// that table does not exist (decision 0044, "No cross-module foreign keys").
func TestAccStreamsMigrateAlone(t *testing.T) {
	mods := sqlcommon.Modules()
	for _, m := range mods {
		t.Run(m.Name, func(t *testing.T) {
			ctx := t.Context()
			db := openUnmigrated(t, scratchSchema(t)).DB()
			up, err := dialect.Migrate(ctx, db, pgdrv.Dialect, mods, m.Name)
			if err != nil || len(up) == 0 {
				t.Fatalf("Migrate %s = %v, %v", m.Name, up, err)
			}
			got, err := sqltest.Schema(ctx, db, pgdrv.Dialect.Name)
			if err != nil || got == "\n" {
				t.Fatalf("Migrate %s created no table (%v)", m.Name, err)
			}
			down, err := dialect.Rollback(ctx, db, pgdrv.Dialect, mods, m.Name, 0)
			if err != nil || len(down) != len(up) {
				t.Fatalf("Rollback %s = %v, %v; want %v reverted", m.Name, down, err, up)
			}
			got, err = sqltest.Schema(ctx, db, pgdrv.Dialect.Name)
			if err != nil || got != "\n" {
				t.Fatalf("Rollback %s left (%v):\n%s", m.Name, err, got)
			}
		})
	}
}

// scratchSchema creates an empty schema of its own on the test server and
// returns a DSN that selects it. pgx passes an unknown DSN parameter such as
// search_path to the server as a run-time parameter, so every connection of
// a pool opened with the DSN starts in the schema, and current_schema(),
// which the migrator's probe and sqltest.Schema read, names it. The schema
// is dropped when the test ends, because one server serves every test of
// the binary.
func scratchSchema(t *testing.T) string {
	t.Helper()
	dsn := platsqltest.PostgresDSN(t)
	name := "scratch_" + strings.ToLower(rand.Text())
	admin := openUnmigrated(t, dsn)
	if _, err := admin.DB().ExecContext(t.Context(), "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Registered after admin's own cleanup, so it runs before admin closes.
		drop := "DROP SCHEMA " + name + " CASCADE"
		if _, err := admin.DB().ExecContext(context.Background(), drop); err != nil {
			t.Errorf("drop schema %s: %v", name, err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", name)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + name
}

// openUnmigrated opens a store over dsn without migrating, closed when the
// test ends.
func openUnmigrated(t *testing.T, dsn string) *sqlcommon.Store {
	t.Helper()
	s, err := postgres.Open(t.Context(), dsn,
		postgres.Options{Options: sqlcommon.Options{SkipMigrate: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAccRemoteCachePersistence(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	open := func() *sqlcommon.Store {
		s, err := postgres.Open(context.Background(), dsn, postgres.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	sqltest.RemoteLayoutPersistence(t, open(), open)
}

func TestAccRepositoryHeaderSecrets(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	first := true
	sqltest.RepositoryHeaderEnvironmentRename(
		t,
		func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
			s, err := postgres.Open(
				t.Context(),
				dsn,
				postgres.Options{Options: sqlcommon.Options{Keyring: k}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if first {
				sqltest.Reset(t, s.DB(), pgdrv.Dialect)
				first = false
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	)
}

func TestAccRepositoryHeaderMigration(t *testing.T) {
	dsn := platsqltest.PostgresDSN(t)
	first := true
	sqltest.RepositoryHeadersOpen(t, func(t *testing.T, k *crypt.Keyring) *sqlcommon.Store {
		s, err := postgres.Open(
			t.Context(),
			dsn,
			postgres.Options{Options: sqlcommon.Options{Keyring: k}},
		)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			sqltest.Reset(t, s.DB(), pgdrv.Dialect)
			first = false
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
