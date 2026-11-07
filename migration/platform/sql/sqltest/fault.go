package sqltest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Exec runs each statement on db, outside any transaction, and fails t at
// the first that fails, naming the statement. Fault tests use it to change
// a database behind its store's back: a trigger that makes a write fail, a
// dropped table, a value the column type forbids.
func Exec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// Fails marks t failed, and goes on, for each entry of calls whose error is
// nil, reporting it by its key as "<key> succeeded". A fault test names
// every call that the injected fault must make fail and checks them in one
// go.
func Fails(t *testing.T, calls map[string]error) {
	t.Helper()
	for name, err := range calls {
		if err == nil {
			t.Errorf("%s succeeded", name)
		}
	}
}

// Err returns the error of a call that returns a value and an error, so the
// call can stand as an entry of the map that Fails takes.
func Err[V any](_ V, err error) error { return err }

// Err3 returns the error of a call that returns a value, a string and an
// error, the shape of a paged list (the page, the next cursor, the error),
// so the call can stand as an entry of the map that Fails takes.
func Err3[V any](_ V, _ string, err error) error { return err }

// FailTrigger returns the statement that creates a trigger making every
// row-level event (INSERT, UPDATE or DELETE) on table abort with
// "injected", so a test reaches the statement after a successful one. It is
// SQLite's trigger syntax: PostgreSQL has no trigger body in the statement,
// only a trigger function, so fault tests that use it run on SQLite.
func FailTrigger(event, table string) string {
	return "CREATE TRIGGER ops_fail_" + strings.ToLower(event) + "_" + table + " BEFORE " + event +
		" ON " + table + " BEGIN SELECT RAISE(ABORT, 'injected'); END"
}

// DropTable returns the statement that drops table, so the statements that
// touch it fail.
func DropTable(table string) string { return "DROP TABLE " + table }
