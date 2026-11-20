package inmem

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Store is the in-memory implementation of the artifacts module's
// port.Store: the repository, remote cache, item, trash, package, Git LFS
// lock, Bower, build and archived-item stores, the blob and replica indexes
// and the cold tier's indexes. It keeps its rows in tables of a shared
// memtx.Store, under the names of the SQL tables, so every call joins the
// transaction that the caller's context carries, whichever module's store
// began it.
type Store struct {
	*memtx.Store
	storage api.StorageWatch
}

var _ port.Store = (*Store)(nil)

// New returns the artifacts module's stores over db.
func New(db *memtx.Store) *Store { return &Store{Store: db} }

// NewWithStorageWatch installs the application's transaction-local accounting
// watch. Standalone module stores use New because they own no project stream.
func NewWithStorageWatch(db *memtx.Store, watch api.StorageWatch) *Store {
	return &Store{Store: db, storage: watch}
}

func (s *Store) storageWrite(ctx context.Context, fn func(context.Context) error) error {
	if s.storage == nil {
		return s.Do(ctx, func() error { return fn(ctx) })
	}
	return s.Tx(ctx, fn)
}

// LayoutWatch returns the config watch that fences remote publication when
// a layout document is created, changed or deleted: every remote repository
// that uses the layout as its own or its upstream layout gets a new source
// generation (storekit.FenceLayoutSources). The composition registers it
// with the platform's config store, which calls it inside the config
// write's transaction, so the edit and the fence commit or roll back
// together (decision 0007).
func (s *Store) LayoutWatch() configdoc.Watch {
	return configdoc.Watch{
		Kind: configdoc.ConfigLayout,
		Changed: func(ctx context.Context, key string) error {
			return storekit.FenceLayoutSources(ctx, s, remoteRecords{s}, key)
		},
	}
}
