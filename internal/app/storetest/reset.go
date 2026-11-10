package storetest

import (
	"database/sql"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// Reset rolls the stream of every module that appsql.Modules registers back
// and applies them all again, leaving an empty schema. Running it before each
// contract test also exercises every down migration on every run.
func Reset(t *testing.T, db *sql.DB, d dialect.Dialect) {
	t.Helper()
	platsqltest.Reset(t, db, d, appsql.Modules())
}
