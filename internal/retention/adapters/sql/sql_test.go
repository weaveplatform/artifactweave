package sql_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	retsql "github.com/weaveplatform/artifactweave/internal/retention/adapters/sql"
	retstoretest "github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{retsql.Module}, retsql.New,
		func(t *testing.T, newStore func(*testing.T) *retsql.Store) {
			retstoretest.Run(t, func(t *testing.T) port.Store { return newStore(t) })
		})
}
