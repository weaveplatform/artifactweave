// Package inmem is the reference in-memory implementation of pkg/store.
//
// # Design
//
// Store embeds a *memtx.Store, the in-memory unit of work, so the gate,
// the transactions with their snapshots, the row copies and the paging are
// memtx's, and so are Tx, AfterCommit and Close. Each entity store here keeps
// its rows in memtx tables and makes every call through Do; the platform's
// stores are migration/platform/store/inmem's, over the same memtx.Store. It
// serves unit tests and embedders that need no durability.
package inmem

import (
	"context"

	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	platinmem "github.com/weaveplatform/artifactweave/migration/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store is an in-memory store.Store. The zero value is not usable; call New.
// The platform's stores are a field over the same memtx.Store rather than
// embedded, so none of their methods is promoted onto Store, where it could
// shadow an accessor without a compile error.
type Store struct {
	*memtx.Store
	platform *platinmem.Store
}

var _ store.Store = (*Store)(nil)

// New returns an empty store. It registers the layout fence with the
// platform's config store: a layout document that is created, changed or
// deleted fences the remote repositories that use it, in the same
// transaction (decision 0007).
func New() *Store {
	s := &Store{Store: memtx.New()}
	s.platform = platinmem.New(s.Store, configdoc.Watch{
		Kind: configdoc.ConfigLayout,
		Changed: func(ctx context.Context, key string) error {
			return store.FenceLayoutSources(ctx, s, remoteRecords{s}, key)
		},
	})
	return s
}

// Settings implements store.Store with the platform's settings store.
func (s *Store) Settings() store.SettingsStore { return s.platform.Settings() }

// Leases implements store.Store with the platform's lease store.
func (s *Store) Leases() store.LeaseStore { return s.platform.Leases() }

// WorkQueue implements store.Store with the platform's work queue.
func (s *Store) WorkQueue() store.WorkQueue { return s.platform.WorkQueue() }

// Configs implements store.Store with the platform's config document store,
// which fences layouts as New registered.
func (s *Store) Configs() store.ConfigStore { return s.platform.Configs() }

// Instance implements store.Store with the platform's instance store.
func (s *Store) Instance() store.InstanceStore { return s.platform.Instance() }
