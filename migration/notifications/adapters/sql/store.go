package sql

import (
	"strconv"

	"github.com/weaveplatform/artifactweave/migration/notifications/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Store is the SQL implementation of the notifications module's stores. It
// is built over a shared dialect.Store, so every call joins the transaction
// that the caller's context carries, whichever module's store began it.
type Store struct{ *dialect.Store }

// New returns the notifications module's stores over db, which must hold
// the module stream's tables.
func New(db *dialect.Store) *Store { return &Store{Store: db} }

// Module registers the notifications module with the SQL layer: its
// migration stream, under the name that keys the stream's rows in
// schema_migrations, and the sealed column its webhook store writes. The
// column keeps the purpose and the row id the store seals with, because
// both are authenticated with every value (decision 0017): a change to
// either makes the stored secrets unreadable.
var Module = dialect.Module{
	Name:       "notifications",
	Migrations: migrations.FS,
	Sealed: []dialect.SealedColumn{
		{
			Table: "webhook_secrets", Column: "value", Purpose: purposeWebhookSecret,
			Keys: []string{"webhook_key", "handler_index", "name"},
			RowID: func(k []string) string {
				i, _ := strconv.Atoi(k[1]) // an INTEGER column always reads back as digits
				return secretID(k[0], i, k[2])
			},
		},
	},
}
