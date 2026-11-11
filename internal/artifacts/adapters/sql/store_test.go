package sql_test

import (
	"context"
	"strings"
	"testing"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// mods is the artifacts module's registration alone: its stream, its sealed
// columns and the repository header backfill. The stream references no
// other stream's tables, so it migrates on its own.
var mods = []dialect.Module{artsql.Module}

// open returns this package's stores over a fresh, migrated SQLite
// database in memory, sealed with the harness's test keyring; t.Cleanup
// closes it.
func open(t *testing.T) *artsql.Store {
	t.Helper()
	return artsql.New(sqltest.OpenSQLite(t, mods...))
}

// testKeys is a keyring under the master key the harness's test keyring
// uses, for the tests that open one database more than once.
func testKeys(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// otherKeys is a version-1 keyring under a different master key.
func otherKeys(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New([]byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// openFile opens the artifacts stores over the SQLite database at path as
// the metadata store's open does for the module, so a test can close a
// database and open it again: the stream migrated, the pool wrapped with
// keys (nil for none), then the module's AfterMigrate. It returns the
// migration's or the hook's error, so a test can watch an open fail, and
// then closes the pool, as the metadata store's open does; otherwise
// t.Cleanup closes it.
func openFile(t *testing.T, path string, keys *crypt.Keyring) (*artsql.Store, error) {
	t.Helper()
	s := rawFile(t, path, keys)
	ctx := t.Context()
	if _, err := dialect.Migrate(ctx, s.DB(), sqlite.Dialect, mods, ""); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := artsql.Module.AfterMigrate(ctx, s.Store); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// mustOpenFile is openFile for a database that must open.
func mustOpenFile(t *testing.T, path string, keys *crypt.Keyring) *artsql.Store {
	t.Helper()
	s, err := openFile(t, path, keys)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// rawFile opens the artifacts stores over the SQLite database at path
// without migrating it or running the hook; t.Cleanup closes the pool.
func rawFile(t *testing.T, path string, keys *crypt.Keyring) *artsql.Store {
	t.Helper()
	db, err := sqlite.OpenDB(path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return artsql.New(dialect.New(db, sqlite.Dialect, keys))
}

// reopen returns a second store over s's database, sealed with keys, after
// running the module's AfterMigrate again, as reopening the database does.
// The adapter keeps no state of its own, so what the second store reads is
// what the rows hold; the pool stays open, so the suites that RunModule
// hands a database in memory can reopen it too.
func reopen(t *testing.T, s *artsql.Store, keys *crypt.Keyring) *artsql.Store {
	t.Helper()
	db := dialect.New(s.DB(), s.Dialect(), keys)
	if err := artsql.Module.AfterMigrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return artsql.New(db)
}

// reseal re-seals the module's sealed columns under the keyring's active
// version, as the metadata store's Reseal does for every module's.
func reseal(ctx context.Context, s *artsql.Store) (int, error) {
	return dialect.Reseal(ctx, s.Store, mods)
}
