package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
)

// openDB opens path through OpenDB, closed when the test ends.
func openDB(t *testing.T, path string, o sqlite.Options) *sql.DB {
	t.Helper()
	db, err := sqlite.OpenDB(path, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// mustExec runs a statement on the pool, outside any transaction.
func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), q); err != nil {
		t.Fatal(err)
	}
}

// code returns err's SQLite result code, or -1 when err is not SQLite's.
func code(err error) int {
	var e *msqlite.Error
	if !errors.As(err, &e) {
		return -1
	}
	return e.Code()
}

func TestAccIsUniqueViolation(t *testing.T) {
	if sqlite.IsUniqueViolation(errors.New("plain")) {
		t.Fatal("a plain error is not a unique violation")
	}
}

// The store maps what IsUniqueViolation reports to kernel.ErrConflict, so it
// must recognise the extended codes this driver returns for a duplicate key,
// unique or primary, and no other constraint failure, which stays a driver
// error.
func TestAccIsUniqueViolationReadsTheConstraintCodes(t *testing.T) {
	ctx := t.Context()
	db := openDB(t, sqlite.Memory, sqlite.Options{})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE)")
	mustExec(t, db, "INSERT INTO t (id, name) VALUES (1, 'a')")
	for _, c := range []struct {
		insert string
		code   int
		unique bool
	}{
		{"INSERT INTO t (id, name) VALUES (2, 'a')", sqlite3.SQLITE_CONSTRAINT_UNIQUE, true},
		{"INSERT INTO t (id, name) VALUES (1, 'b')", sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, true},
		{"INSERT INTO t (id, name) VALUES (3, NULL)", sqlite3.SQLITE_CONSTRAINT_NOTNULL, false},
	} {
		_, err := db.ExecContext(ctx, c.insert)
		if code(err) != c.code {
			t.Fatalf("%s: %v, want SQLite code %d", c.insert, err, c.code)
		}
		if got := sqlite.IsUniqueViolation(err); got != c.unique {
			t.Errorf("%s: IsUniqueViolation(%v) = %t, want %t", c.insert, err, got, c.unique)
		}
	}
}

func TestUnitOpenDBRefusesAnEmptyPath(t *testing.T) {
	db, err := sqlite.OpenDB("", sqlite.Options{})
	if db != nil || !errors.Is(err, sqlite.ErrPathRequired) {
		t.Fatalf("OpenDB(\"\") = %v, %v; want ErrPathRequired", db, err)
	}
	if got := err.Error(); got != "sqlite: path is required" {
		t.Errorf("Error() = %q", got)
	}
}

// fixedPragmas are the pragmas of every DSN after its busy timeout, in order.
var fixedPragmas = []string{"journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"}

// The DSN text reaches modernc's parser unchanged, so the default case pins
// it byte for byte; the others check the parts a busy timeout changes.
func TestUnitDSNCarriesThePragmas(t *testing.T) {
	const want = "file:/data/aw.db?_pragma=busy_timeout%2810000%29&_pragma=journal_mode%28WAL%29" +
		"&_pragma=foreign_keys%281%29&_pragma=synchronous%28NORMAL%29&_txlock=immediate"
	if got := sqlite.DSN("/data/aw.db", sqlite.Options{}); got != want {
		t.Errorf("DSN = %q\nwant  %q", got, want)
	}
	for _, c := range []struct {
		name    string
		timeout time.Duration
		pragma  string
	}{
		{"zero is 10s", 0, "busy_timeout(10000)"},
		{"negative is 10s", -time.Second, "busy_timeout(10000)"},
		{"set", 2500 * time.Millisecond, "busy_timeout(2500)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dsn := sqlite.DSN("aw.db", sqlite.Options{BusyTimeout: c.timeout})
			path, query, ok := strings.Cut(dsn, "?")
			if !ok || path != "file:aw.db" {
				t.Fatalf("DSN = %q, want file:aw.db and a query", dsn)
			}
			q, err := url.ParseQuery(query)
			if err != nil {
				t.Fatal(err)
			}
			pragmas := append([]string{c.pragma}, fixedPragmas...)
			if got := q["_pragma"]; !slices.Equal(got, pragmas) {
				t.Errorf("_pragma = %q, want %q", got, pragmas)
			}
			if got := q["_txlock"]; !slices.Equal(got, []string{"immediate"}) {
				t.Errorf("_txlock = %q, want immediate", got)
			}
			if len(q) != 2 {
				t.Errorf("query has %d keys, want _pragma and _txlock only: %q", len(q), query)
			}
		})
	}
}

// Each connection to ":memory:" opens a database of its own, so the pool must
// never open a second connection, which would find none of the tables. The
// test holds the pool's connection and checks that a statement on the pool
// waits for it rather than opening another.
func TestAccOpenDBKeepsMemoryOnOneConnection(t *testing.T) {
	ctx := t.Context()
	db := openDB(t, sqlite.Memory, sqlite.Options{})
	if n := db.Stats().MaxOpenConnections; n != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", n)
	}
	mustExec(t, db, "CREATE TABLE t (v INTEGER)")
	mustExec(t, db, "INSERT INTO t (v) VALUES (42)")
	var v int
	if err := db.QueryRowContext(ctx, "SELECT v FROM t").Scan(&v); err != nil || v != 42 {
		t.Fatalf("read %d, %v; want 42 from the table an earlier statement created", v, err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err = db.QueryRowContext(wait, "SELECT v FROM t").Scan(&v)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a statement while the connection is held: %v; want it to wait", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT v FROM t").Scan(&v); err != nil || v != 42 {
		t.Fatalf("read %d, %v after the connection returned; want 42", v, err)
	}
	if n := db.Stats().OpenConnections; n != 1 {
		t.Errorf("OpenConnections = %d, want 1", n)
	}
}

// modernc applies the DSN's pragmas to every new connection, so the test reads
// them back rather than trusting the DSN text, from a file database because an
// in-memory one reports its journal mode as memory.
func TestAccOpenDBAppliesThePragmas(t *testing.T) {
	for _, c := range []struct {
		name string
		o    sqlite.Options
		busy string
	}{
		{"default", sqlite.Options{}, "10000"},
		{"busy timeout set", sqlite.Options{BusyTimeout: 1500 * time.Millisecond}, "1500"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := openDB(t, filepath.Join(t.TempDir(), "aw.db"), c.o)
			for _, p := range []struct{ pragma, want string }{
				{"journal_mode", "wal"},
				{"foreign_keys", "1"},
				{"synchronous", "1"}, // NORMAL
				{"busy_timeout", c.busy},
			} {
				var got string
				row := db.QueryRowContext(t.Context(), "PRAGMA "+p.pragma)
				if err := row.Scan(&got); err != nil {
					t.Fatalf("PRAGMA %s: %v", p.pragma, err)
				}
				if got != p.want {
					t.Errorf("PRAGMA %s = %q, want %q", p.pragma, got, p.want)
				}
			}
		})
	}
}

// _txlock=immediate makes BEGIN take the write lock at once, so writers queue
// at BEGIN for up to the busy timeout instead of failing busy at their first
// write, as two deferred transactions that both read first can. A second
// BEGIN must therefore wait out the timeout and fail busy while the first
// transaction holds the lock, though it has written nothing.
func TestAccOpenDBBeginsImmediate(t *testing.T) {
	ctx := t.Context()
	o := sqlite.Options{BusyTimeout: 50 * time.Millisecond}
	db := openDB(t, filepath.Join(t.TempDir(), "aw.db"), o)
	first, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback() }()
	second, err := db.BeginTx(ctx, nil)
	if err == nil {
		_ = second.Rollback()
		t.Fatal("a second transaction began while the first held the write lock")
	}
	if code(err)&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("second BEGIN: %v; want SQLITE_BUSY", err)
	}
}
