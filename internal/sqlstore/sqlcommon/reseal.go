package sqlcommon

import (
	"context"
	"strconv"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

var sealedColumns = []sealedColumn{
	{"repository_secrets", "password", purposeRepoPassword, []string{"repo_key"}, firstKey},
	{
		"repository_header_secrets",
		"value",
		purposeRepoHeader,
		[]string{"repo_key", "position", "name"},
		func(k []string) string { index, _ := strconv.Atoi(k[1]); return repositoryHeaderID(k[0], index, k[2]) },
	},
	{"keypairs", "private_key", purposeKeypairPrivate, []string{"name"}, firstKey},
	{"keypairs", "passphrase", purposeKeypairPassphrase, []string{"name"}, firstKey},
	{"signing_keys", "private_key", purposeSigningPrivate, []string{"alias"}, firstKey},
	{"signing_keys", "passphrase", purposeSigningPassphrase, []string{"alias"}, firstKey},
	{"certificates", "private_key", purposeCertificateKey, []string{"alias"}, firstKey},
	{"tokens", "secret", purposeTokenSecret, []string{"id"}, firstKey},
	{"replications", "password", purposeReplicationPassword, []string{"replication_key"}, firstKey},
	{
		"webhook_secrets", "value", purposeWebhookSecret,
		[]string{"webhook_key", "handler_index", "name"},
		func(k []string) string {
			i, _ := strconv.Atoi(k[1]) // an INTEGER column always reads back as digits
			return secretID(k[0], i, k[2])
		},
	},
	{
		"config_secrets", "value", purposeConfigSecret,
		[]string{"kind", "key", "name"},
		func(k []string) string { return configSecretID(types.ConfigKind(k[0]), k[1], k[2]) },
	},
	{
		"worker_secrets", "value", purposeWorkerSecret,
		[]string{"worker_key", "name"},
		func(k []string) string { return k[0] + "/" + k[1] },
	},
	{"instance", "private_key", purposeInstanceKey, []string{"instance_id"}, firstKey},
}

// Reseal re-seals every sealed value under the keyring's active version and
// returns how many values changed. Values already under the active version
// are left alone, and values stored before a keyring was configured are
// sealed for the first time, so running it again is harmless. Each value is
// replaced only if it still holds the bytes that were read, so a concurrent
// write is never overwritten with a stale secret.
func (s *Store) Reseal(ctx context.Context) (int, error) {
	if !s.HasKeyring() {
		return 0, ErrNoKeyring
	}
	changed, err := s.backfillRepositoryHeaders(ctx)
	if err != nil {
		return changed, err
	}
	for _, c := range sealedColumns {
		n, err := s.resealColumn(ctx, c)
		changed += n
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
