// Package postgres opens an artifactweave store on PostgreSQL through pgx's
// database/sql adapter (decision 0004).
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
)

// Options configures Open.
type Options struct {
	sqlcommon.Options
	// MaxOpenConns bounds the pool; 0 keeps the database/sql default.
	MaxOpenConns int
	// ConnMaxLifetime recycles connections; 0 keeps them.
	ConnMaxLifetime time.Duration
}

// Open connects to dsn and migrates the schema.
func Open(ctx context.Context, dsn string, o Options) (*sqlcommon.Store, error) {
	db, err := OpenDB(dsn, o)
	if err != nil {
		return nil, err
	}
	s, err := sqlcommon.Open(ctx, db, Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return s, nil
}
