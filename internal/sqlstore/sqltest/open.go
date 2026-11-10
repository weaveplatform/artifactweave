package sqltest

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
)

// SQLStore is the composite SQL store: every module's SQL store over one
// unit of work, as the server opens it. A module's tests may import only
// test support from app, never the composite's own packages, so they name
// the composite, and open it with OpenSQLite and OpenPostgres, through this
// package.
type SQLStore = sqlcommon.Store

// Options configures the composite store's open: whether it migrates, and
// the keyring that seals secret columns.
type Options = sqlcommon.Options

// SQLiteOptions configures OpenSQLite: the composite store's Options and
// the SQLite driver's busy timeout.
type SQLiteOptions = sqlite.Options

// PostgresOptions configures OpenPostgres: the composite store's Options and
// the PostgreSQL driver's pool settings.
type PostgresOptions = postgres.Options

// OpenSQLite opens the composite store on the SQLite database at path,
// creating it if needed, exactly as the server's SQLite open does: the
// database is migrated unless o.SkipMigrate, the pool is closed on failure,
// and the errors are the server's.
func OpenSQLite(ctx context.Context, path string, o SQLiteOptions) (*SQLStore, error) {
	//nolint:wrapcheck // the server's open errors, which tests match as they are
	return sqlite.Open(ctx, path, o)
}

// OpenPostgres opens the composite store on the PostgreSQL database that
// dsn names, exactly as the server's PostgreSQL open does: the database is
// migrated unless o.SkipMigrate, the pool is closed on failure, and the
// errors are the server's.
func OpenPostgres(ctx context.Context, dsn string, o PostgresOptions) (*SQLStore, error) {
	//nolint:wrapcheck // the server's open errors, which tests match as they are
	return postgres.Open(ctx, dsn, o)
}
