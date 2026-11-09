package sql_test

import (
	"testing"

	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	artmigrations "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// stream is the artifacts module's migration stream under the module's
// name. The registration's sealed columns and hooks belong to the
// repository code, which is still the metadata store's, and the tests here
// touch only tables of this package's stores, which the stream creates.
var stream = dialect.Module{Name: "artifacts", Migrations: artmigrations.FS}

// open returns this package's stores over a fresh, migrated SQLite
// database; t.Cleanup closes it.
func open(t *testing.T) *artsql.Store {
	t.Helper()
	return artsql.New(sqltest.OpenSQLite(t, stream))
}
