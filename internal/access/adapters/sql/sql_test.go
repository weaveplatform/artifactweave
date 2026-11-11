package sql_test

import (
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	accstoretest "github.com/weaveplatform/artifactweave/internal/access/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			accstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
