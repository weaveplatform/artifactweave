package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// ErrPathRequired is returned by OpenDB for an empty path.
var ErrPathRequired = errors.New("sqlite: path is required")

// Memory is the path that opens a private in-memory database, which lives as
// long as the pool that OpenDB returns for it.
const Memory = ":memory:"

// Dialect is SQLite's dialect.Dialect, with IsUniqueViolation: "?"
// placeholders, and no row-lock suffix, because IMMEDIATE transactions
// already serialise writers.
var Dialect = dialect.Dialect{
	Name: "sqlite",
	// The table-valued form of PRAGMA table_info, so the table name binds
	// as a parameter.
	Columns:           "SELECT name FROM pragma_table_info(?)",
	IsUniqueViolation: IsUniqueViolation,
}

// IsUniqueViolation reports whether err is, or wraps, a SQLite unique or
// primary-key constraint failure, which the SQL store turns into
// kernel.ErrConflict. Any other error, another constraint failure included,
// reports false.
func IsUniqueViolation(err error) bool {
	var e *msqlite.Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
		e.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// Options configures DSN and OpenDB.
type Options struct {
	// BusyTimeout bounds lock waits; 10s when zero.
	BusyTimeout time.Duration
}

// DSN returns the modernc connection string that OpenDB opens for path: the
// busy timeout, 10s unless o sets one, WAL journaling, foreign keys,
// synchronous NORMAL and IMMEDIATE transactions.
func DSN(path string, o Options) string {
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 10 * time.Second
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(o.BusyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// OpenDB returns a pool over the SQLite database at path, through the
// "sqlite" driver that modernc.org/sqlite registers, with the settings of
// DSN. It does not connect: the pool's first connection creates a missing
// database file, and a path that cannot be opened fails there. Memory opens a
// private in-memory database on one connection that the pool never closes
// for age or idleness, because each connection to ":memory:" sees its own
// empty database. OpenDB returns ErrPathRequired for an empty path. It does
// not ping, migrate or wrap the pool in a store; the caller closes the pool.
func OpenDB(path string, o Options) (*sql.DB, error) {
	if path == "" {
		return nil, ErrPathRequired
	}
	db, err := sql.Open("sqlite", DSN(path, o))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if path == Memory {
		db.SetMaxOpenConns(1)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
	}
	return db, nil
}
