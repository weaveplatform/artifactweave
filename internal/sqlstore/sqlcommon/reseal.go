package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// sealedColumn is one column holding sealed values: the purpose and the row
// id it is sealed with (decision 0017), and the key columns that identify
// its rows. Every column a store seals must be listed here, or
// `artifactweave keys rotate` leaves its values under the old key and they
// become unreadable once that key is removed; TestSealedColumnsComplete
// fails when a sealed column is missing.
type sealedColumn struct {
	table, column, purpose string
	keys                   []string
	rowID                  func(key []string) string
}

func firstKey(k []string) string { return k[0] }

var sealedColumns = []sealedColumn{
	{"repository_secrets", "password", purposeRepoPassword, []string{"repo_key"}, firstKey},
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

// sealedRow is one value read for re-sealing, with its key values as the
// driver returned them so the UPDATE binds them with their own types.
type sealedRow struct {
	key   []any
	value []byte
}

// Reseal re-seals every sealed value under the keyring's active version and
// returns how many values changed. Values already under the active version
// are left alone, and values stored before a keyring was configured are
// sealed for the first time, so running it again is harmless. Each value is
// replaced only if it still holds the bytes that were read, so a concurrent
// write is never overwritten with a stale secret.
func (s *Store) Reseal(ctx context.Context) (int, error) {
	if s.keys == nil {
		return 0, ErrNoKeyring
	}
	changed := 0
	for _, c := range sealedColumns {
		n, err := s.resealColumn(ctx, c)
		changed += n
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func (s *Store) resealColumn(ctx context.Context, c sealedColumn) (int, error) {
	op := "reseal " + c.table + "." + c.column
	// Read everything first: an in-memory SQLite database has one
	// connection, which an open result set would hold while updating.
	var rows []sealedRow
	err := s.rows(ctx, op,
		"SELECT "+strings.Join(c.keys, ", ")+", "+c.column+" FROM "+c.table+
			" WHERE "+c.column+" IS NOT NULL",
		nil,
		func(r *sql.Rows) error {
			row := sealedRow{key: make([]any, len(c.keys))}
			dest := make([]any, 0, len(c.keys)+1)
			for i := range row.key {
				dest = append(dest, &row.key[i])
			}
			if err := r.Scan(append(dest, &row.value)...); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			rows = append(rows, row)
			return nil
		})
	if err != nil {
		return 0, err
	}
	where := make([]string, 0, len(c.keys)+1)
	for _, k := range c.keys {
		where = append(where, k+" = ?")
	}
	update := "UPDATE " + c.table + " SET " + c.column + " = ? WHERE " +
		strings.Join(append(where, c.column+" = ?"), " AND ")
	changed := 0
	for _, row := range rows {
		id := c.rowID(keyStrings(row.key))
		next, did, err := s.resealValue(c.purpose, id, row.value)
		if err != nil {
			return changed, fmt.Errorf("%s %q: %w", op, id, err)
		}
		if !did {
			continue
		}
		args := append(append([]any{next}, row.key...), row.value)
		n, err := s.exec(ctx, op, update, args...)
		if err != nil {
			return changed, err
		}
		changed += int(n)
	}
	return changed, nil
}

// resealValue re-seals one value, sealing a plain one for the first time.
func (s *Store) resealValue(purpose, rowID string, v []byte) ([]byte, bool, error) {
	if !crypt.IsSealed(v) {
		sealed, err := s.keys.Seal(purpose, rowID, v)
		return sealed, err == nil, err //nolint:wrapcheck // wrapped by the caller
	}
	return s.keys.Reseal(purpose, rowID, v) //nolint:wrapcheck // wrapped by the caller
}

// keyStrings renders scanned key values as the strings row ids are built
// from.
func keyStrings(key []any) []string {
	out := make([]string, len(key))
	for i, v := range key {
		if b, ok := v.([]byte); ok {
			out[i] = string(b)
			continue
		}
		out[i] = fmt.Sprint(v)
	}
	return out
}
