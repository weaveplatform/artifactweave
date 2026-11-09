package sql_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
	wrksql "github.com/weaveplatform/artifactweave/migration/workers/adapters/sql"
	wrkstoretest "github.com/weaveplatform/artifactweave/migration/workers/adapters/storetest"
	"github.com/weaveplatform/artifactweave/migration/workers/core/port"
)

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{wrksql.Module}, wrksql.New,
		func(t *testing.T, newStore func(*testing.T) *wrksql.Store) {
			wrkstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
