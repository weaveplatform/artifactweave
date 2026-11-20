package inmem

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Store is the in-memory implementation of the access module's stores. It
// keeps its rows in tables of a shared memtx.Store, under the names of the
// SQL tables, so every call joins the transaction that the caller's context
// carries, whichever module's store began it.
type Store struct {
	*memtx.Store
	storagePolicy func(context.Context, string) error
}

// New returns the access module's stores over db.
func New(db *memtx.Store) *Store { return &Store{Store: db} }

// NewWithStorageWatch binds project quota-policy changes to the application's
// root accounting transaction. Standalone access stores have no artifact totals.
func NewWithStorageWatch(db *memtx.Store, changed func(context.Context, string) error) *Store {
	return &Store{Store: db, storagePolicy: changed}
}

func (s *Store) projectWrite(ctx context.Context, fn func(context.Context) error) error {
	if s.storagePolicy == nil {
		return s.Do(ctx, func() error { return fn(ctx) })
	}
	return s.Tx(ctx, fn)
}

func (s *Store) storageChanged(ctx context.Context, key string) error {
	if s.storagePolicy == nil {
		return nil
	}
	return s.storagePolicy(ctx, key) //nolint:wrapcheck // the root accounting watch names its failure
}
