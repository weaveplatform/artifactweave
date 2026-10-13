// Package sqlite opens an artifactweave store on SQLite through the pure-Go
// modernc.org/sqlite driver, so the server builds without CGO on every
// platform (decision 0004).
//
// Transactions begin IMMEDIATE, taking the write lock up front; with WAL and
// a busy timeout this serialises writers without deadlocks, and readers keep
// reading while one writes.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/weaveplatform/weaveplatform-artifact-registry/internal/sqlstore/sqlcommon"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrPathRequired is returned by Open for an empty path.
var ErrPathRequired = errors.New("sqlite: path is required")

// Memory is the path that opens a private in-memory database.
const Memory = ":memory:"

// Dialect is SQLite's sqlcommon.Dialect.
var Dialect = sqlcommon.Dialect{
	Name:              "sqlite",
	Migrations:        sqlcommon.MustSub(migrationFS, "migrations"),
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

// Options configures Open.
type Options struct {
	sqlcommon.Options
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

// Open opens (creating if needed) and migrates the database at path. The
// path Memory opens a private in-memory database, held on one connection
// because each connection to ":memory:" would otherwise see its own empty
// database.
func Open(ctx context.Context, path string, o Options) (*sqlcommon.Store, error) {
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
	s, err := sqlcommon.Open(ctx, db, Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	return s, nil
}
