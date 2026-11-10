package sql_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	relsql "github.com/weaveplatform/artifactweave/internal/releases/adapters/sql"
	relstoretest "github.com/weaveplatform/artifactweave/internal/releases/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/releases/core/port"
)

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{relsql.Module}, relsql.New,
		func(t *testing.T, newStore func(*testing.T) *relsql.Store) {
			relstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
