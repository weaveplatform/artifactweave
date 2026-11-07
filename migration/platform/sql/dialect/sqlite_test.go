package dialect_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// sqliteDialect is as much of SQLite's dialect as these tests need. The
// backend's own value is in a legacy package, which the dialect layer's tests
// do not import.
var sqliteDialect = dialect.Dialect{Name: "sqlite"}

// openSQLite opens a database file of its own in t's temporary directory. A
// file, unlike ":memory:", is one database for every connection of the pool.
func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dialect.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// mustExec runs a statement on the pool, outside any transaction.
func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

// count returns the number of rows of table.
func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// hasTable reports whether the database has a table called name.
func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}
