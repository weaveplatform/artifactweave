package sql_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/store/storetest"
)

// withWatches is a platform store still to be given its watches: RunModule
// opens the database, and the contract's Factory names the watches.
type withWatches func(watches ...configdoc.Watch) *platsql.Store

func TestAccContract(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module},
		func(db *dialect.Store) withWatches {
			return func(watches ...configdoc.Watch) *platsql.Store { return platsql.New(db, watches...) }
		},
		func(t *testing.T, newStore func(*testing.T) withWatches) {
			storetest.Run(t, func(t *testing.T, watches ...configdoc.Watch) storetest.Store {
				return newStore(t)(watches...)
			})
		})
}
