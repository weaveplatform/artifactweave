package sql_test

import (
	"testing"

	fedsql "github.com/weaveplatform/artifactweave/migration/federation/adapters/sql"
	fedstoretest "github.com/weaveplatform/artifactweave/migration/federation/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
)

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{fedsql.Module}, fedsql.New,
		func(t *testing.T, newStore func(*testing.T) *fedsql.Store) {
			fedstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
