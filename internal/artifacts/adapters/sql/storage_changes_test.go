package sql_test

import (
	"testing"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccStorageChanges(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New, func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
		storetest.RunStorageChanges(t, func(t *testing.T, watch api.StorageWatch) port.Store {
			return artsql.NewWithStorageWatch(newStore(t).Store, watch)
		})
	}, sqltest.WithPostgres())
}
