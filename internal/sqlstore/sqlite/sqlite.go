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
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	sqlitedrv "github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
)

// Options configures Open.
type Options struct {
	sqlcommon.Options
	// BusyTimeout bounds lock waits; 10s when zero.
	BusyTimeout time.Duration
}

// Open opens (creating if needed) and migrates the database at path. The
// path Memory opens a private in-memory database, held on one connection
// because each connection to ":memory:" would otherwise see its own empty
// database.
func Open(ctx context.Context, path string, o Options) (*sqlcommon.Store, error) {
	db, err := sqlitedrv.OpenDB(path, sqlitedrv.Options{BusyTimeout: o.BusyTimeout})
	if err != nil {
		return nil, err
	}
	s, err := sqlcommon.Open(ctx, db, sqlitedrv.Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	return s, nil
}
