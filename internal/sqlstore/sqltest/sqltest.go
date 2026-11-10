// Package sqltest holds the suites that the SQLite and PostgreSQL store
// packages run against the whole composite store: key rotation (Rotation,
// with its Opener); the parts of the repository header and remote cache
// suites that need more than the artifacts module's stores, whose tests run
// the rest (an environment rename through the roles service, the header
// conversion at the composite's open with its reseal, and a layout edit
// through the config store); the replay of a crashed write, the refusal of
// a legacy migration history, and the schema description that the golden
// files and the stream checks compare (Schema, SchemaGolden). Reset empties
// a composite store's database for the next test by rolling back, and
// migrating again, every stream that sqlcommon.Modules lists. They stay
// here until STO-19 moves the composite store's suites.
package sqltest

import (
	"database/sql"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// Reset rolls the stream of every module that sqlcommon.Modules registers
// back and applies them all again, leaving an empty schema. Running it
// before each contract test also exercises every down migration on every
// run.
func Reset(t *testing.T, db *sql.DB, d dialect.Dialect) {
	t.Helper()
	platsqltest.Reset(t, db, d, sqlcommon.Modules())
}
