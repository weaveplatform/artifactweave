package sqlcommon

import (
	"context"
	"strconv"

	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// modules returns every module's registration with the SQL layer, in the
// canonical module order that Reseal follows (decision 0044,
// "Persistence"). Each sealed column keeps the purpose and the row id its
// store seals with, which are authenticated with the value (decision 0017):
// a change to either makes every stored value of the column unreadable.
// It is a function rather than a variable, so no caller can change the
// list, and a hook can reach code that reads it without an initialisation
// cycle.
func modules() []dialect.Module {
	return []dialect.Module{
		{Name: "platform", Sealed: []dialect.SealedColumn{
			{
				Table: "config_secrets", Column: "value", Purpose: purposeConfigSecret,
				Keys: []string{"kind", "key", "name"},
				RowID: func(k []string) string {
					return configSecretID(types.ConfigKind(k[0]), k[1], k[2])
				},
			},
			{
				Table: "instance", Column: "private_key", Purpose: purposeInstanceKey,
				Keys: []string{"instance_id"}, RowID: dialect.FirstKey,
			},
		}},
		{
			Name: "artifacts",
			// The backfill moves the sensitive headers still held in legacy
			// repository documents into sealed rows, and counts them, before
			// the module's columns are re-sealed.
			BeforeReseal: func(ctx context.Context, s *dialect.Store) (int, error) {
				return (&Store{Store: s}).backfillRepositoryHeaders(ctx)
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
		{Name: "access", Sealed: []dialect.SealedColumn{
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
		}},
		{Name: "notifications", Sealed: []dialect.SealedColumn{
			{
				Table: "webhook_secrets", Column: "value", Purpose: purposeWebhookSecret,
				Keys: []string{"webhook_key", "handler_index", "name"},
				RowID: func(k []string) string {
					i, _ := strconv.Atoi(k[1]) // an INTEGER column always reads back as digits
					return secretID(k[0], i, k[2])
				},
			},
		}},
		{Name: "retention"},
		{Name: "federation", Sealed: []dialect.SealedColumn{
			{
				Table: "replications", Column: "password", Purpose: purposeReplicationPassword,
				Keys: []string{"replication_key"}, RowID: dialect.FirstKey,
			},
		}},
		{Name: "releases"},
		{Name: "workers", Sealed: []dialect.SealedColumn{
			{
				Table: "worker_secrets", Column: "value", Purpose: purposeWorkerSecret,
				Keys:  []string{"worker_key", "name"},
				RowID: func(k []string) string { return k[0] + "/" + k[1] },
			},
		}},
	}
}

// Reseal re-seals every sealed value under the keyring's active version and
// returns how many values changed. Values already under the active version
// are left alone, and values stored before a keyring was configured are
// sealed for the first time, so running it again is harmless. Each value is
// replaced only if it still holds the bytes that were read, so a concurrent
// write is never overwritten with a stale secret.
func (s *Store) Reseal(ctx context.Context) (int, error) {
	return dialect.Reseal(ctx, s.Store, modules())
}
