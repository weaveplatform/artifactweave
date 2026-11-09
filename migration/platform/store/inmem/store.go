package inmem

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// Store is the in-memory implementation of the platform's stores. It keeps
// its rows in tables of a shared memtx.Store, under the names of the SQL
// tables, so every call joins the transaction that the caller's context
// carries, whichever store began it.
type Store struct {
	*memtx.Store
	watches []configdoc.Watch
}

// New returns the platform's stores over db. Its config store calls
// watches as configdoc.Watch describes.
func New(db *memtx.Store, watches ...configdoc.Watch) *Store {
	return &Store{Store: db, watches: slices.Clone(watches)}
}

// notify runs the watches of kind for key.
func (s *Store) notify(ctx context.Context, kind configdoc.ConfigKind, key string) error {
	for _, w := range s.watches {
		if w.Kind != kind {
			continue
		}
		if err := w.Changed(ctx, key); err != nil {
			return err //nolint:wrapcheck // the watch's error names its operation
		}
	}
	return nil
}

// limitOr applies the default limit for "at most limit" queries.
func limitOr(limit int) int {
	if limit <= 0 {
		return kernel.DefaultLimit
	}
	return min(limit, kernel.MaxLimit)
}
