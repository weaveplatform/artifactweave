package sql_test

import (
	"testing"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccPrincipalProjectLookup(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New, func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
		storetest.RunPrincipalProjects(t, newStore(t))
	}, sqltest.WithPostgres())
}
