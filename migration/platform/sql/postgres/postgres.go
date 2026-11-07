package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// ErrDSNRequired is returned by Open for an empty DSN.
var ErrDSNRequired = errors.New("postgres: dsn is required")

// uniqueViolation is SQLSTATE 23505.
const uniqueViolation = "23505"

// Dialect is PostgreSQL's dialect.Dialect.
var Dialect = dialect.Dialect{
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

// Options configures OpenDB.
type Options struct {
	// MaxOpenConns bounds the pool; 0 keeps the database/sql default.
	MaxOpenConns int
	// ConnMaxLifetime recycles connections; 0 keeps them.
	ConnMaxLifetime time.Duration
}

// OpenDB opens the pool for dsn, sized and recycled as o says. It returns
// ErrDSNRequired for an empty DSN, and does not connect, migrate or wrap
// the pool in a store.
func OpenDB(dsn string, o Options) (*sql.DB, error) {
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
	return db, nil
}
