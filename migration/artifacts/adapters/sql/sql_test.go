package sql_test

import (
	"testing"

	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	artstoretest "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

// TestAccContract runs the artifacts contract suite on the module's SQL
// adapter alone, on SQLite in memory and in a file; the metadata store's
// SQLite and PostgreSQL runs keep running it through the composite.
func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			artstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
