// Package postgres opens the composite artifactweave store on PostgreSQL: the
// pool of the platform/sql/postgres driver, migrated and wrapped in the SQL
// store.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	pgdrv "github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"
)

// Options configures Open: the store's options, and the driver's, which Open
// passes on.
type Options struct {
	sql.Options
	// MaxOpenConns bounds the pool; 0 keeps the database/sql default.
	MaxOpenConns int
	// ConnMaxLifetime recycles connections; 0 keeps them.
	ConnMaxLifetime time.Duration
}

// Open opens the composite store on the PostgreSQL database that dsn names:
// the driver's pool with o's pool settings, which sqlcommon.Open pings,
// migrates unless o.SkipMigrate, wraps in the SQL store and closes on
// failure. The driver's errors, ErrDSNRequired among them, are returned as
// they are, and sqlcommon.Open's behind "postgres: ".
func Open(ctx context.Context, dsn string, o Options) (*sql.Store, error) {
	db, err := pgdrv.OpenDB(dsn, pgdrv.Options{
		MaxOpenConns:    o.MaxOpenConns,
		ConnMaxLifetime: o.ConnMaxLifetime,
	})
	if err != nil {
		return nil, err
	}
	s, err := sql.Open(ctx, db, pgdrv.Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return s, nil
}
