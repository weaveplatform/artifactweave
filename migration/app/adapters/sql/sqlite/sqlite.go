// Package sqlite opens the composite artifactweave store on SQLite: the pool
// of the platform/sql/sqlite driver, migrated and wrapped in the SQL store.
package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	sqlitedrv "github.com/weaveplatform/artifactweave/migration/platform/sql/sqlite"
)

// Options configures Open: the store's options, and the driver's, which Open
// passes on.
type Options struct {
	sql.Options
	// BusyTimeout bounds lock waits; 10s when zero.
	BusyTimeout time.Duration
}

// Open opens the composite store on the SQLite database at path, creating it
// if needed: the driver's pool with o's busy timeout, which sqlcommon.Open
// pings, migrates unless o.SkipMigrate, wraps in the SQL store and closes on
// failure. The path Memory opens a private in-memory database. The driver's
// errors, ErrPathRequired among them, are returned as they are, and
// sqlcommon.Open's behind "sqlite: ".
func Open(ctx context.Context, path string, o Options) (*sql.Store, error) {
	db, err := sqlitedrv.OpenDB(path, sqlitedrv.Options{BusyTimeout: o.BusyTimeout})
	if err != nil {
		return nil, err
	}
	s, err := sql.Open(ctx, db, sqlitedrv.Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	return s, nil
}
