// Package sqltest provides PostgreSQL for the store contract runs. It
// starts one container per test binary with testcontainers-go and skips the
// calling test where Docker cannot run Linux containers (decision 0015), so
// the suite runs everywhere and the PostgreSQL half runs in the Linux CI
// jobs. Set ARTIFACTWEAVE_TEST_POSTGRES_DSN to use an existing server
// instead.
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
