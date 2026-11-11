package sql_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	wrksql "github.com/weaveplatform/artifactweave/internal/workers/adapters/sql"
	wrkstoretest "github.com/weaveplatform/artifactweave/internal/workers/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/workers/core/port"
)

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{wrksql.Module}, wrksql.New,
		func(t *testing.T, newStore func(*testing.T) *wrksql.Store) {
			wrkstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
