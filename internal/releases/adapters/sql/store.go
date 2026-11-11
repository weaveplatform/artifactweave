package sql

import (
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/releases/adapters/sql/migrations"
)

// Store is the SQL implementation of the releases module's stores. It is
// built over a shared dialect.Store, so every call joins the transaction
// that the caller's context carries, whichever module's store began it.
type Store struct{ *dialect.Store }

// New returns the releases module's stores over db, which must hold the
// module stream's tables.
func New(db *dialect.Store) *Store { return &Store{Store: db} }

// Module registers the releases module with the SQL layer: its migration
// stream, under the name that keys the stream's rows in schema_migrations.
// The module seals no column.
var Module = dialect.Module{Name: "releases", Migrations: migrations.FS}
