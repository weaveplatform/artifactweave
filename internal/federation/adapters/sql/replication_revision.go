package sql

import (
	"context"
	"crypto/rand"
	"database/sql"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// backfillReplicationRevisions never opens credentials or changes target keys.
// Each bounded page commits independently; reopening preserves allocated values.
func (s *Store) backfillReplicationRevisions(ctx context.Context) error {
	for {
		var keys []string
		err := s.Tx(ctx, func(ctx context.Context) error {
			if err := s.Rows(ctx, "read replication revision backfill",
				"SELECT replication_key FROM replications WHERE private_revision = '' ORDER BY replication_key LIMIT ?"+s.LockClause(ctx),
				[]any{kernel.MaxLimit}, func(row *sql.Rows) error {
					var key string
					if err := row.Scan(&key); err != nil {
						return err //nolint:wrapcheck // Rows supplies operation context
					}
					keys = append(keys, key)
					return nil
				}); err != nil {
				return err
			}
			for _, key := range keys {
				if _, err := s.Exec(ctx, "backfill replication revision",
					"UPDATE replications SET private_revision = ? WHERE replication_key = ? AND private_revision = ''",
					rand.Text(), key); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil || len(keys) < kernel.MaxLimit {
			return err
		}
	}
}
