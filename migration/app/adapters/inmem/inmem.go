package inmem

import (
	accinmem "github.com/weaveplatform/artifactweave/migration/access/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	artinmem "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	fedinmem "github.com/weaveplatform/artifactweave/migration/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
	ntfinmem "github.com/weaveplatform/artifactweave/migration/notifications/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/migration/platform/outbox"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	platinmem "github.com/weaveplatform/artifactweave/migration/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	relinmem "github.com/weaveplatform/artifactweave/migration/releases/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/releases/core/port"
	retinmem "github.com/weaveplatform/artifactweave/migration/retention/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/retention/core/port"
	wrkinmem "github.com/weaveplatform/artifactweave/migration/workers/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/workers/core/port"
)

// Store is the in-memory composite store. The zero value is not usable; call
// New. The platform's and the modules' stores are fields over the same
// memtx.Store rather than embedded, so none of their methods is promoted onto
// Store, where it could shadow an accessor without a compile error.
type Store struct {
	*memtx.Store
	platform      *platinmem.Store
	artifacts     *artinmem.Store
	access        *accinmem.Store
	notifications *ntfinmem.Store
	retention     *retinmem.Store
	federation    *fedinmem.Store
	releases      *relinmem.Store
	workers       *wrkinmem.Store
}

// New returns an empty store. It registers the artifacts module's layout
// watch with the platform's config store: a layout document that is
// created, changed or deleted fences the remote repositories that use it,
// in the same transaction (decision 0007).
func New() *Store {
	s := &Store{Store: memtx.New()}
	s.artifacts = artinmem.New(s.Store)
	s.platform = platinmem.New(s.Store, s.artifacts.LayoutWatch())
	s.access = accinmem.New(s.Store)
	s.notifications = ntfinmem.New(s.Store)
	s.retention = retinmem.New(s.Store)
	s.federation = fedinmem.New(s.Store)
	s.releases = relinmem.New(s.Store)
	s.workers = wrkinmem.New(s.Store)
	return s
}

// Settings is the platform's settings store.
func (s *Store) Settings() settings.SettingsStore { return s.platform.Settings() }

// Leases is the platform's lease store.
func (s *Store) Leases() work.LeaseStore { return s.platform.Leases() }

// WorkQueue is the platform's work queue.
func (s *Store) WorkQueue() work.WorkQueue { return s.platform.WorkQueue() }

// Configs is the platform's config document store, which fences layouts as New
// registered.
func (s *Store) Configs() configdoc.ConfigStore { return s.platform.Configs() }

// Instance is the platform's instance store.
func (s *Store) Instance() instance.InstanceStore { return s.platform.Instance() }

// Outbox is the platform's federation outbox store.
func (s *Store) Outbox() outbox.OutboxStore { return s.platform.Outbox() }

// Uploads is the platform's upload session store.
func (s *Store) Uploads() session.UploadStore { return s.platform.Uploads() }

// Replays is the platform's API replay store.
func (s *Store) Replays() idempotency.ReplayStore { return s.platform.Replays() }

// RequestBudgets is the platform's request budget store.
func (s *Store) RequestBudgets() apilimit.RequestBudgetStore { return s.platform.RequestBudgets() }

// Repos is the artifacts module's repository store.
func (s *Store) Repos() port.RepoStore { return s.artifacts.Repos() }

// RemoteCache is the artifacts module's remote cache store.
func (s *Store) RemoteCache() port.RemoteCacheStore { return s.artifacts.RemoteCache() }

// Items is the artifacts module's item store.
func (s *Store) Items() port.ItemStore { return s.artifacts.Items() }

// Trash is the artifacts module's trash store.
func (s *Store) Trash() port.TrashStore { return s.artifacts.Trash() }

// Packages is the artifacts module's package store.
func (s *Store) Packages() port.PackageStore { return s.artifacts.Packages() }

// Blobs is the artifacts module's blob index.
func (s *Store) Blobs() port.BlobIndex { return s.artifacts.Blobs() }

// Replicas is the artifacts module's replica index.
func (s *Store) Replicas() port.ReplicaIndex { return s.artifacts.Replicas() }

// GitLFSLocks is the artifacts module's Git LFS lock store.
func (s *Store) GitLFSLocks() port.GitLFSLockStore { return s.artifacts.GitLFSLocks() }

// BowerRegistrations is the artifacts module's Bower registration store.
func (s *Store) BowerRegistrations() port.BowerRegistrationStore {
	return s.artifacts.BowerRegistrations()
}

// Builds is the artifacts module's build store.
func (s *Store) Builds() port.BuildStore { return s.artifacts.Builds() }

// ArchivedItems is the artifacts module's archived-item store.
func (s *Store) ArchivedItems() port.ArchivedItemStore { return s.artifacts.ArchivedItems() }

// ColdBlobs is the artifacts module's blob index over the archive tier.
func (s *Store) ColdBlobs() port.BlobIndex { return s.artifacts.ColdBlobs() }

// ColdReplicas is the artifacts module's replica index over the archive tier.
func (s *Store) ColdReplicas() port.ReplicaIndex { return s.artifacts.ColdReplicas() }

// Users is the access module's user store.
func (s *Store) Users() port.UserStore { return s.access.Users() }

// Groups is the access module's group store.
func (s *Store) Groups() port.GroupStore { return s.access.Groups() }

// Permissions is the access module's permission store.
func (s *Store) Permissions() port.PermissionStore { return s.access.Permissions() }

// Tokens is the access module's token store.
func (s *Store) Tokens() port.TokenStore { return s.access.Tokens() }

// Keys is the access module's key store.
func (s *Store) Keys() port.KeyStore { return s.access.Keys() }

// Projects is the access module's project store.
func (s *Store) Projects() port.ProjectStore { return s.access.Projects() }

// APIRestrictions is the access module's API restriction store.
func (s *Store) APIRestrictions() port.APIRestrictionStore { return s.access.APIRestrictions() }

// Webhooks is the notifications module's webhook store.
func (s *Store) Webhooks() port.WebhookStore { return s.notifications.Webhooks() }

// Cleanup is the retention module's cleanup store.
func (s *Store) Cleanup() port.CleanupStore { return s.retention.Cleanup() }

// Archive is the retention module's archive store.
func (s *Store) Archive() port.ArchiveStore { return s.retention.Archive() }

// Replications is the federation module's replication store.
func (s *Store) Replications() port.ReplicationStore { return s.federation.Replications() }

// Peers is the federation module's peer store.
func (s *Store) Peers() port.PeerStore { return s.federation.Peers() }

// Federation is the federation module's federated repository store.
func (s *Store) Federation() port.FederationStore { return s.federation.Federation() }

// Platform is the federation module's platform federation store.
func (s *Store) Platform() port.PlatformStore { return s.federation.Platform() }

// Bundles is the releases module's bundle store.
func (s *Store) Bundles() port.BundleStore { return s.releases.Bundles() }

// Distribution is the releases module's distribution store.
func (s *Store) Distribution() port.DistributionStore { return s.releases.Distribution() }

// Workers is the workers module's worker store.
func (s *Store) Workers() port.WorkerStore { return s.workers.Workers() }
