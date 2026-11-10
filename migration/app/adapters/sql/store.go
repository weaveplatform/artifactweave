package sql

import (
	"context"
	"database/sql"
	"fmt"

	accsql "github.com/weaveplatform/artifactweave/migration/access/adapters/sql"
	accport "github.com/weaveplatform/artifactweave/migration/access/core/port"
	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	artport "github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	fedsql "github.com/weaveplatform/artifactweave/migration/federation/adapters/sql"
	fedport "github.com/weaveplatform/artifactweave/migration/federation/core/port"
	ntfsql "github.com/weaveplatform/artifactweave/migration/notifications/adapters/sql"
	ntfport "github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/migration/platform/outbox"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	platsql "github.com/weaveplatform/artifactweave/migration/platform/store/sql"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	relsql "github.com/weaveplatform/artifactweave/migration/releases/adapters/sql"
	relport "github.com/weaveplatform/artifactweave/migration/releases/core/port"
	retsql "github.com/weaveplatform/artifactweave/migration/retention/adapters/sql"
	retport "github.com/weaveplatform/artifactweave/migration/retention/core/port"
	wrksql "github.com/weaveplatform/artifactweave/migration/workers/adapters/sql"
	wrkport "github.com/weaveplatform/artifactweave/migration/workers/core/port"
)

// Store is the composite SQL store over database/sql for one Dialect. The
// embedded dialect store holds the pool, the dialect and the keyring and runs
// the unit of work; Store adds an accessor for each store of the platform and
// the modules. The platform's and the modules' stores are fields over the same
// dialect store rather than embedded, so none of their methods is promoted
// onto Store, where it could shadow an accessor without a compile error.
type Store struct {
	*dialect.Store
	platform      *platsql.Store
	artifacts     *artsql.Store
	access        *accsql.Store
	notifications *ntfsql.Store
	retention     *retsql.Store
	federation    *fedsql.Store
	releases      *relsql.Store
	workers       *wrksql.Store
}

// Options configures Open.
type Options struct {
	// SkipMigrate opens without applying migrations.
	SkipMigrate bool
	// Keyring seals secret columns. Without one they are stored in plain
	// text. Enabling a keyring later requires Reseal to convert existing values.
	Keyring *crypt.Keyring
}

// Open pings db and wraps it. Unless o.SkipMigrate, it first applies the
// pending migrations of every module's stream and then runs each module's
// AfterMigrate hook, in the canonical module order of Modules. It closes db
// on failure. Each backend opens its pool and calls it.
func Open(ctx context.Context, db *sql.DB, d dialect.Dialect, o Options) (*Store, error) {
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	mods := Modules()
	if !o.SkipMigrate {
		if _, err := dialect.Migrate(ctx, db, d, mods, ""); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	s := New(db, d, o.Keyring)
	if !o.SkipMigrate {
		for _, m := range mods {
			if m.AfterMigrate == nil {
				continue
			}
			if err := m.AfterMigrate(ctx, s.Store); err != nil {
				_ = db.Close()
				return nil, err //nolint:wrapcheck // the module's store names the operation
			}
		}
	}
	return s, nil
}

// New wraps an open, migrated pool. It registers the artifacts module's
// layout watch with the platform's config store: a layout document that is
// created, changed or deleted fences the remote repositories that use it,
// in the same transaction (decision 0007).
func New(db *sql.DB, d dialect.Dialect, keys *crypt.Keyring) *Store {
	s := &Store{Store: dialect.New(db, d, keys)}
	s.artifacts = artsql.New(s.Store)
	s.platform = platsql.New(s.Store, s.artifacts.LayoutWatch())
	s.access = accsql.New(s.Store)
	s.notifications = ntfsql.New(s.Store)
	s.retention = retsql.New(s.Store)
	s.federation = fedsql.New(s.Store)
	s.releases = relsql.New(s.Store)
	s.workers = wrksql.New(s.Store)
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
func (s *Store) Repos() artport.RepoStore { return s.artifacts.Repos() }

// RemoteCache is the artifacts module's remote cache store.
func (s *Store) RemoteCache() artport.RemoteCacheStore { return s.artifacts.RemoteCache() }

// Items is the artifacts module's item store.
func (s *Store) Items() artport.ItemStore { return s.artifacts.Items() }

// Trash is the artifacts module's trash store.
func (s *Store) Trash() artport.TrashStore { return s.artifacts.Trash() }

// Packages is the artifacts module's package store.
func (s *Store) Packages() artport.PackageStore { return s.artifacts.Packages() }

// Blobs is the artifacts module's blob index.
func (s *Store) Blobs() artport.BlobIndex { return s.artifacts.Blobs() }

// Replicas is the artifacts module's replica index.
func (s *Store) Replicas() artport.ReplicaIndex { return s.artifacts.Replicas() }

// GitLFSLocks is the artifacts module's Git LFS lock store.
func (s *Store) GitLFSLocks() artport.GitLFSLockStore { return s.artifacts.GitLFSLocks() }

// BowerRegistrations is the artifacts module's Bower registration store.
func (s *Store) BowerRegistrations() artport.BowerRegistrationStore {
	return s.artifacts.BowerRegistrations()
}

// Builds is the artifacts module's build store.
func (s *Store) Builds() artport.BuildStore { return s.artifacts.Builds() }

// ArchivedItems is the artifacts module's archived-item store.
func (s *Store) ArchivedItems() artport.ArchivedItemStore { return s.artifacts.ArchivedItems() }

// ColdBlobs is the artifacts module's blob index over the archive tier.
func (s *Store) ColdBlobs() artport.BlobIndex { return s.artifacts.ColdBlobs() }

// ColdReplicas is the artifacts module's replica index over the archive tier.
func (s *Store) ColdReplicas() artport.ReplicaIndex { return s.artifacts.ColdReplicas() }

// Users is the access module's user store.
func (s *Store) Users() accport.UserStore { return s.access.Users() }

// Groups is the access module's group store.
func (s *Store) Groups() accport.GroupStore { return s.access.Groups() }

// Permissions is the access module's permission store.
func (s *Store) Permissions() accport.PermissionStore { return s.access.Permissions() }

// Tokens is the access module's token store.
func (s *Store) Tokens() accport.TokenStore { return s.access.Tokens() }

// Keys is the access module's key store.
func (s *Store) Keys() accport.KeyStore { return s.access.Keys() }

// Projects is the access module's project store.
func (s *Store) Projects() accport.ProjectStore { return s.access.Projects() }

// APIRestrictions is the access module's API restriction store.
func (s *Store) APIRestrictions() accport.APIRestrictionStore { return s.access.APIRestrictions() }

// Webhooks is the notifications module's webhook store.
func (s *Store) Webhooks() ntfport.WebhookStore { return s.notifications.Webhooks() }

// Cleanup is the retention module's cleanup store.
func (s *Store) Cleanup() retport.CleanupStore { return s.retention.Cleanup() }

// Archive is the retention module's archive store.
func (s *Store) Archive() retport.ArchiveStore { return s.retention.Archive() }

// Replications is the federation module's replication store.
func (s *Store) Replications() fedport.ReplicationStore { return s.federation.Replications() }

// Peers is the federation module's peer store.
func (s *Store) Peers() fedport.PeerStore { return s.federation.Peers() }

// Federation is the federation module's federated repository store.
func (s *Store) Federation() fedport.FederationStore { return s.federation.Federation() }

// Platform is the federation module's platform federation store.
func (s *Store) Platform() fedport.PlatformStore { return s.federation.Platform() }

// Bundles is the releases module's bundle store.
func (s *Store) Bundles() relport.BundleStore { return s.releases.Bundles() }

// Distribution is the releases module's distribution store.
func (s *Store) Distribution() relport.DistributionStore { return s.releases.Distribution() }

// Workers is the workers module's worker store.
func (s *Store) Workers() wrkport.WorkerStore { return s.workers.Workers() }
