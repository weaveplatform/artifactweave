package sqltest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
)

// testKey is the master key of the test keyring: the key the composite
// store's contract runs seal with, so a module run seals the same way.
const testKey = "0123456789abcdef0123456789abcdef"

// Reset rolls every stream of mods back to zero and migrates them again,
// leaving their tables empty; tables of other streams are untouched.
// Running it before each test also exercises every down migration of mods.
func Reset(t *testing.T, db *sql.DB, d dialect.Dialect, mods []dialect.Module) {
	t.Helper()
	ctx := context.Background()
	if _, err := dialect.Rollback(ctx, db, d, mods, "", 0); err != nil {
		t.Fatalf("reset rollback: %v", err)
	}
	if _, err := dialect.Migrate(ctx, db, d, mods, ""); err != nil {
		t.Fatalf("reset migrate: %v", err)
	}
}

// Option configures RunModule.
type Option func(*options)

type options struct {
	postgres bool
	keys     *crypt.Keyring
}

// WithPostgres adds the "postgres" subtest to RunModule. PostgreSQL is asked
// for rather than run by default because module suites run on SQLite and
// the PostgreSQL runs stay with the composite store (decision 0044,
// "Persistence").
func WithPostgres() Option { return func(o *options) { o.postgres = true } }

// WithKeyring makes RunModule seal its stores with k in place of the test
// keyring. A nil k opens them without a keyring: Seal then stores values as
// given, and Unseal fails on a sealed value with dialect.ErrNoKeyring.
func WithKeyring(k *crypt.Keyring) Option { return func(o *options) { o.keys = k } }

// RunModule runs suite in the subtests "memory" and "file", SQLite in memory
// and in a file of the test's temporary directory, and, with WithPostgres,
// "postgres". In each, newStore returns open applied to a fresh database
// that holds only the tables of mods: every call opens a database of its
// own, migrates every stream of mods, wraps it in a store sealed with the
// test keyring or WithKeyring's, and runs each module's AfterMigrate in the
// order of mods, as the composite store's open does. mods are in the
// canonical module order, as dialect.Migrate takes them.
//
// On PostgreSQL every store gets a scratch schema of its own (ScratchDSN) on
// the server PostgresDSN returns, so no store sees another's rows or the
// tables that other tests keep in the server's public schema, even when
// test processes share one server; the subtest skips where PostgresDSN
// skips. A store is closed, and its schema dropped, when the test that
// called newStore ends. A database that cannot be opened or migrated, or a
// hook that fails, fails that test.
func RunModule[S any](
	t *testing.T,
	mods []dialect.Module,
	open func(*dialect.Store) S,
	suite func(t *testing.T, newStore func(*testing.T) S),
	opts ...Option,
) {
	t.Helper()
	o := options{keys: testKeyring(t)}
	for _, opt := range opts {
		opt(&o)
	}
	newStore := func(d dialect.Dialect, newDB func(*testing.T) *sql.DB) func(*testing.T) S {
		return func(t *testing.T) S {
			t.Helper()
			return open(migrated(t, newDB(t), d, mods, o.keys))
		}
	}
	t.Run("memory", func(t *testing.T) {
		suite(t, newStore(sqlite.Dialect, func(t *testing.T) *sql.DB {
			return sqliteDB(t, sqlite.Memory)
		}))
	})
	t.Run("file", func(t *testing.T) {
		suite(t, newStore(sqlite.Dialect, func(t *testing.T) *sql.DB {
			return sqliteDB(t, filepath.Join(t.TempDir(), "module.db"))
		}))
	})
	if !o.postgres {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		dsn := PostgresDSN(t)
		suite(t, newStore(postgres.Dialect, func(t *testing.T) *sql.DB {
			return postgresDB(t, ScratchDSN(t, dsn))
		}))
	})
}

// OpenSQLite returns a fresh in-memory SQLite store migrated with mods and
// sealed with the test keyring, with each module's AfterMigrate run as
// RunModule runs it; t.Cleanup closes it. A database that cannot be
// migrated, or a hook that fails, fails t.
func OpenSQLite(t *testing.T, mods ...dialect.Module) *dialect.Store {
	t.Helper()
	return migrated(t, sqliteDB(t, sqlite.Memory), sqlite.Dialect, mods, testKeyring(t))
}

// migrated follows the composite store's open step by step, so a module
// store starts from the state the server would give it, the conversions of
// AfterMigrate included.
func migrated(
	t *testing.T, db *sql.DB, d dialect.Dialect, mods []dialect.Module, keys *crypt.Keyring,
) *dialect.Store {
	t.Helper()
	ctx := t.Context()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := dialect.Migrate(ctx, db, d, mods, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := dialect.New(db, d, keys)
	for _, m := range mods {
		if m.AfterMigrate == nil {
			continue
		}
		if err := m.AfterMigrate(ctx, s); err != nil {
			t.Fatalf("%s after migrate: %v", m.Name, err)
		}
	}
	return s
}

func sqliteDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sqlite.OpenDB(path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testKeyring(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte(testKey))
	if err != nil {
		t.Fatalf("test keyring: %v", err)
	}
	return k
}
