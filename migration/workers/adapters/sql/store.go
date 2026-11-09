package sql

import (
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/workers/adapters/sql/migrations"
)

// Store is the SQL implementation of the workers module's stores. It is
// built over a shared dialect.Store, so every call joins the transaction
// that the caller's context carries, whichever module's store began it.
type Store struct{ *dialect.Store }

// New returns the workers module's stores over db, which must hold the
// module stream's tables.
func New(db *dialect.Store) *Store { return &Store{Store: db} }

// Module registers the workers module with the SQL layer: its migration
// stream, under the name that keys the stream's rows in schema_migrations,
// and the sealed column its worker store writes. The column keeps the
// purpose and the row id the store seals with, because both are
// authenticated with every value (decision 0017): a change to either makes
// the stored secrets unreadable.
var Module = dialect.Module{
	Name:       "workers",
	Migrations: migrations.FS,
	Sealed: []dialect.SealedColumn{
		{
			Table: "worker_secrets", Column: "value", Purpose: purposeWorkerSecret,
			Keys:  []string{"worker_key", "name"},
			RowID: func(k []string) string { return k[0] + "/" + k[1] },
		},
	},
}
