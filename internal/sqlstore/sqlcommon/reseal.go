package sqlcommon

import (
	"context"

	accsql "github.com/weaveplatform/artifactweave/migration/access/adapters/sql"
	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	fedsql "github.com/weaveplatform/artifactweave/migration/federation/adapters/sql"
	ntfsql "github.com/weaveplatform/artifactweave/migration/notifications/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	platsql "github.com/weaveplatform/artifactweave/migration/platform/store/sql"
	relsql "github.com/weaveplatform/artifactweave/migration/releases/adapters/sql"
	retsql "github.com/weaveplatform/artifactweave/migration/retention/adapters/sql"
	wrksql "github.com/weaveplatform/artifactweave/migration/workers/adapters/sql"
)

// Modules returns every module's registration with the SQL layer, in the
// canonical module order that the migrator, Open's AfterMigrate hooks and
// Reseal follow (decision 0044, "Persistence"): each module's migration
// stream, its sealed columns and its hooks. The names key the modules' rows
// in schema_migrations, so a renamed module would find no history and apply
// its baseline again. Each sealed column keeps the purpose and the row id its
// store seals with, which are authenticated with the value (decision 0017):
// a change to either makes every stored value of the column unreadable.
// It is a function rather than a variable, so no caller can change the
// list, and a hook can reach code that reads it without an initialisation
// cycle.
func Modules() []dialect.Module {
	return []dialect.Module{
		platsql.Module,
		artsql.Module,
		accsql.Module,
		ntfsql.Module,
		retsql.Module,
		fedsql.Module,
		relsql.Module,
		wrksql.Module,
	}
}

// Reseal re-seals every sealed value under the keyring's active version and
// returns how many values changed. Values already under the active version
// are left alone, and values stored before a keyring was configured are
// sealed for the first time, so running it again is harmless. Each value is
// replaced only if it still holds the bytes that were read, so a concurrent
// write is never overwritten with a stale secret.
func (s *Store) Reseal(ctx context.Context) (int, error) {
	return dialect.Reseal(ctx, s.Store, Modules())
}
