package sql

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/access/adapters/sql/migrations"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// Store is the SQL implementation of the access module's stores. It is
// built over a shared dialect.Store, so every call joins the transaction
// that the caller's context carries, whichever module's store began it.
type Store struct{ *dialect.Store }

// New returns the access module's stores over db, which must hold the
// module stream's tables.
func New(db *dialect.Store) *Store { return &Store{Store: db} }

// Module registers the access module with the SQL layer: its migration
// stream, under the name that keys the stream's rows in schema_migrations;
// the identity-search backfill, which runs after every migration; and the
// sealed columns its token and key stores write. Each column keeps the
// purpose and the row id the store seals with, because both are
// authenticated with every value (decision 0017): a change to either makes
// the stored secrets unreadable.
var Module = dialect.Module{
	Name:       "access",
	Migrations: migrations.FS,
	// Go fills the lowercase search names that a row lacks, because
	// the engines fold case by their own rules (decision 0043).
	AfterMigrate: func(ctx context.Context, s *dialect.Store) error {
		return New(s).backfillIdentitySearch(ctx)
	},
	Sealed: []dialect.SealedColumn{
		{
			Table: "keypairs", Column: "private_key", Purpose: purposeKeypairPrivate,
			Keys: []string{"name"}, RowID: dialect.FirstKey,
		},
		{
			Table: "keypairs", Column: "passphrase", Purpose: purposeKeypairPassphrase,
			Keys: []string{"name"}, RowID: dialect.FirstKey,
		},
		{
			Table: "signing_keys", Column: "private_key", Purpose: purposeSigningPrivate,
			Keys: []string{"alias"}, RowID: dialect.FirstKey,
		},
		{
			Table: "signing_keys", Column: "passphrase", Purpose: purposeSigningPassphrase,
			Keys: []string{"alias"}, RowID: dialect.FirstKey,
		},
		{
			Table: "certificates", Column: "private_key", Purpose: purposeCertificateKey,
			Keys: []string{"alias"}, RowID: dialect.FirstKey,
		},
		{
			Table: "tokens", Column: "secret", Purpose: purposeTokenSecret,
			Keys: []string{"id"}, RowID: dialect.FirstKey,
		},
	},
}
