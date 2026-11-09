// Package inmem is the reference in-memory implementation of pkg/store.
//
// # Design
//
// Store embeds a *memtx.Store, the in-memory unit of work, so the gate, the
// transactions with their snapshots, the row copies and the paging are
// memtx's, and so are Tx, AfterCommit and Close. Each entity store here
// keeps its rows in memtx tables and makes every call through Do; the
// platform's stores are migration/platform/store/inmem's, and the
// notifications, retention, federation, releases and workers stores are
// those modules' adapters/inmem's, all over the same memtx.Store. It serves
// unit tests and embedders that need no durability.
package inmem

import (
	"context"

	fedinmem "github.com/weaveplatform/artifactweave/migration/federation/adapters/inmem"
	ntfinmem "github.com/weaveplatform/artifactweave/migration/notifications/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	platinmem "github.com/weaveplatform/artifactweave/migration/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	relinmem "github.com/weaveplatform/artifactweave/migration/releases/adapters/inmem"
	retinmem "github.com/weaveplatform/artifactweave/migration/retention/adapters/inmem"
	wrkinmem "github.com/weaveplatform/artifactweave/migration/workers/adapters/inmem"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store is an in-memory store.Store. The zero value is not usable; call New.
// The platform's and the modules' stores are fields over the same
// memtx.Store rather than embedded, so none of their methods is promoted
// onto Store, where it could shadow an accessor without a compile error.
type Store struct {
	*memtx.Store
	platform      *platinmem.Store
	notifications *ntfinmem.Store
	retention     *retinmem.Store
	federation    *fedinmem.Store
	releases      *relinmem.Store
	workers       *wrkinmem.Store
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
	s.notifications = ntfinmem.New(s.Store)
	s.retention = retinmem.New(s.Store)
	s.federation = fedinmem.New(s.Store)
	s.releases = relinmem.New(s.Store)
	s.workers = wrkinmem.New(s.Store)
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

// Outbox implements store.Store with the platform's federation outbox store.
func (s *Store) Outbox() store.OutboxStore { return s.platform.Outbox() }

// Uploads implements store.Store with the platform's upload session store.
func (s *Store) Uploads() store.UploadStore { return s.platform.Uploads() }

// Replays implements store.Store with the platform's API replay store.
func (s *Store) Replays() store.ReplayStore { return s.platform.Replays() }

// RequestBudgets implements store.Store with the platform's request budget
// store.
func (s *Store) RequestBudgets() store.RequestBudgetStore { return s.platform.RequestBudgets() }

// Webhooks implements store.Store with the notifications module's webhook
// store.
func (s *Store) Webhooks() store.WebhookStore { return s.notifications.Webhooks() }

// Cleanup implements store.Store with the retention module's cleanup store.
func (s *Store) Cleanup() store.CleanupStore { return s.retention.Cleanup() }

// Archive implements store.Store with the retention module's archive store.
func (s *Store) Archive() store.ArchiveStore { return s.retention.Archive() }

// Replications implements store.Store with the federation module's
// replication store.
func (s *Store) Replications() store.ReplicationStore { return s.federation.Replications() }

// Peers implements store.Store with the federation module's peer store.
func (s *Store) Peers() store.PeerStore { return s.federation.Peers() }

// Federation implements store.Store with the federation module's federated
// repository store.
func (s *Store) Federation() store.FederationStore { return s.federation.Federation() }

// Platform implements store.Store with the federation module's platform
// federation store.
func (s *Store) Platform() store.PlatformStore { return s.federation.Platform() }

// Bundles implements store.Store with the releases module's bundle store.
func (s *Store) Bundles() store.BundleStore { return s.releases.Bundles() }

// Distribution implements store.Store with the releases module's
// distribution store.
func (s *Store) Distribution() store.DistributionStore { return s.releases.Distribution() }

// Workers implements store.Store with the workers module's worker store.
func (s *Store) Workers() store.WorkerStore { return s.workers.Workers() }
