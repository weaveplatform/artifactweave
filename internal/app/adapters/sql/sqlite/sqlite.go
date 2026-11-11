package sqlite

import (
	"context"
	"fmt"
	"time"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	sqlitedrv "github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
)

// Options configures Open: the store's options, and the driver's, which Open
// passes on.
type Options struct {
	appsql.Options
	// BusyTimeout bounds lock waits; 10s when zero.
	BusyTimeout time.Duration
}

// Open opens the composite store on the SQLite database at path, creating it
// if needed: the driver's pool with o's busy timeout, which appsql.Open pings,
// migrates unless o.SkipMigrate, wraps in the SQL store and closes on failure.
// The driver's Memory path opens a private in-memory database. The driver's
// errors, ErrPathRequired among them, are returned as they are, and
// appsql.Open's behind "sqlite: ".
func Open(ctx context.Context, path string, o Options) (*appsql.Store, error) {
	db, err := sqlitedrv.OpenDB(path, sqlitedrv.Options{BusyTimeout: o.BusyTimeout})
	if err != nil {
		return nil, err
	}
	s, err := appsql.Open(ctx, db, sqlitedrv.Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	return s, nil
}
