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

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// ErrPathRequired is returned by Open for an empty path.
var ErrPathRequired = errors.New("sqlite: path is required")

// Memory is the path that opens a private in-memory database.
const Memory = ":memory:"

// Dialect is SQLite's dialect.Dialect.
var Dialect = dialect.Dialect{
	Name: "sqlite",
	// The table-valued form of PRAGMA table_info, so the table name binds
	// as a parameter.
	Columns:           "SELECT name FROM pragma_table_info(?)",
	IsUniqueViolation: IsUniqueViolation,
}

// IsUniqueViolation reports SQLite unique and primary-key failures.
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

// DSN builds the modernc connection string for path.
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

// OpenDB opens the pool of the SQLite database at path with the pragmas of
// DSN. The path Memory opens a private in-memory database on one
// connection. It returns ErrPathRequired for an empty path, and does not
// ping, migrate or wrap the pool in a store.
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
