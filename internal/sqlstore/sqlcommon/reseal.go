package sqlcommon

import (
	"context"
	"strconv"

	accmigrations "github.com/weaveplatform/artifactweave/migration/access/adapters/sql/migrations"
	artmigrations "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql/migrations"
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
		{
			Name:       "artifacts",
			Migrations: artmigrations.FS,
			// The backfill moves the sensitive headers still held in legacy
			// repository documents into sealed rows, and counts them, before
			// the module's columns are re-sealed.
			BeforeReseal: func(ctx context.Context, s *dialect.Store) (int, error) {
				return (&Store{Store: s}).backfillRepositoryHeaders(ctx)
			},
			// The same backfill runs at every open, with the configured
			// keyring, because DDL never sees key material (decision 0017).
			AfterMigrate: func(ctx context.Context, s *dialect.Store) error {
				_, err := (&Store{Store: s}).backfillRepositoryHeaders(ctx)
				return err
			},
			Sealed: []dialect.SealedColumn{
				{
					Table: "repository_secrets", Column: "password", Purpose: purposeRepoPassword,
					Keys: []string{"repo_key"}, RowID: dialect.FirstKey,
				},
				{
					Table: "repository_header_secrets", Column: "value", Purpose: purposeRepoHeader,
					Keys: []string{"repo_key", "position", "name"},
					RowID: func(k []string) string {
						index, _ := strconv.Atoi(k[1])
						return repositoryHeaderID(k[0], index, k[2])
					},
				},
			},
		},
		{
			Name:       "access",
			Migrations: accmigrations.FS,
			// Go fills the lowercase search names that a row lacks, because
			// the engines fold case by their own rules (decision 0043).
			AfterMigrate: func(ctx context.Context, s *dialect.Store) error {
				return (&Store{Store: s}).backfillIdentitySearch(ctx)
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
		},
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
