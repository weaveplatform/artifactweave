// Package sqltest holds the suites that the SQLite and PostgreSQL store
// packages run against the whole composite store: key rotation (Rotation,
// with its Opener), repository headers and their migration, remote cache
// persistence, the replay of a crashed write, the refusal of a legacy
// migration history, and the schema description that the golden files and
// the stream checks compare (Schema, SchemaGolden). Reset empties a
// composite store's database for the next test by rolling back, and
// migrating again, every stream that sqlcommon.Modules lists. They stay here
// until STO-19 moves the composite store's suites.
//
// PostgresDSN is a bridge to the module SQL harness in
// migration/platform/sql/sqltest, which starts the one PostgreSQL container
// a test binary shares, or uses the server that
// ARTIFACTWEAVE_TEST_POSTGRES_DSN names, and skips the calling test where
// Docker cannot run Linux containers (decision 0015).
package sqltest

import (
	"database/sql"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// Reset rolls the stream of every module that sqlcommon.Modules registers
// back and applies them all again, leaving an empty schema. Running it
// before each contract test also exercises every down migration on every
// run.
func Reset(t *testing.T, db *sql.DB, d sqlcommon.Dialect) {
	t.Helper()
	platsqltest.Reset(t, db, d, sqlcommon.Modules())
}
