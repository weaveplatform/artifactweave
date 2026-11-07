// Package sqlcommon implements pkg/store over database/sql for any SQL
// dialect (decision 0004), following hostweave's dialect layer: one store
// implementation, a Dialect describing what differs between engines, and
// embedded per-dialect migrations.
//
// Schema conventions shared by every table (research 15 section 4):
//
//   - Times are BIGINT Unix microseconds, NULL for a zero time; durations
//     are BIGINT milliseconds. Integers compare and sort identically on
//     every engine, which keeps keyset pagination exact.
//   - JSON documents are TEXT, so they round-trip byte for byte.
//   - Sealed values are BLOB (BYTEA) columns of their own, never inside a
//     document, sealed with the purpose and row id of decision 0017.
//   - Text columns that lists sort by are byte-ordered (COLLATE "C" on
//     PostgreSQL, SQLite's default), so every backend pages in the order
//     inmem does.
package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

var _ store.Store = (*Store)(nil)

// Options configures Open.
type Options struct {
	// SkipMigrate opens without applying migrations.
	SkipMigrate bool
	// Keyring seals secret columns. Without one they are stored in plain
	// text. Enabling a keyring later requires Reseal to convert existing values.
	Keyring *crypt.Keyring
}

// Open pings db, applies pending migrations unless o.SkipMigrate, and wraps
// it. It closes db on failure. Each backend opens its pool and calls it.
func Open(ctx context.Context, db *sql.DB, d Dialect, o Options) (*Store, error) {
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	if !o.SkipMigrate {
		if _, err := Migrate(ctx, db, d); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	s := New(db, d, o.Keyring)
	if !o.SkipMigrate {
		if _, err := s.backfillRepositoryHeaders(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		if err := s.backfillIdentitySearch(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

// New wraps an open, migrated pool.
func New(db *sql.DB, d Dialect, keys *crypt.Keyring) *Store {
	return &Store{db: db, d: d, keys: keys}
}
