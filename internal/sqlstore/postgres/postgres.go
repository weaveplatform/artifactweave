// Package postgres opens an artifactweave store on PostgreSQL through pgx's
// database/sql adapter (decision 0004).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
)

// ErrDSNRequired is returned by Open for an empty DSN.
var ErrDSNRequired = errors.New("postgres: dsn is required")

// uniqueViolation is SQLSTATE 23505.
const uniqueViolation = "23505"

// Dialect is PostgreSQL's sqlcommon.Dialect.
var Dialect = sqlcommon.Dialect{
	Name:      "postgres",
	Dollar:    true,
	ForUpdate: "FOR UPDATE",
	// current_schema() is the first schema of the search_path, the one an
	// unqualified CREATE TABLE writes to, so a scratch schema selected in
	// the DSN sees only its own history table.
	Columns: "SELECT column_name FROM information_schema.columns " +
		"WHERE table_schema = current_schema() AND table_name = ?",
	IsUniqueViolation: IsUniqueViolation,
}

// IsUniqueViolation reports a PostgreSQL unique violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

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
	if dsn == "" {
		return nil, ErrDSNRequired
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)
	s, err := sqlcommon.Open(ctx, db, Dialect, o.Options)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return s, nil
}
