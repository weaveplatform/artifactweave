package sql_test

import (
	"testing"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccProjectStorageComposition(t *testing.T) {
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), nil)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		appstoretest.RunProjectStorage(t, func(t *testing.T) appstoretest.Store { return newStore(t) })
	}, sqltest.WithPostgres(), sqltest.WithKeyring(nil))
}
