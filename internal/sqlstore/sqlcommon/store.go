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

	accsql "github.com/weaveplatform/artifactweave/migration/access/adapters/sql"
	fedsql "github.com/weaveplatform/artifactweave/migration/federation/adapters/sql"
	ntfsql "github.com/weaveplatform/artifactweave/migration/notifications/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	platsql "github.com/weaveplatform/artifactweave/migration/platform/store/sql"
	relsql "github.com/weaveplatform/artifactweave/migration/releases/adapters/sql"
	retsql "github.com/weaveplatform/artifactweave/migration/retention/adapters/sql"
	wrksql "github.com/weaveplatform/artifactweave/migration/workers/adapters/sql"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store implements store.Store over database/sql for one Dialect. The
// embedded dialect store holds the pool, the dialect and the keyring and
// runs the unit of work; Store adds the aggregate's accessors. The
// platform's and the modules' stores are fields over the same dialect store
// rather than embedded, so none of their methods is promoted onto Store,
// where it could shadow an accessor without a compile error.
type Store struct {
	*dialect.Store
	platform      *platsql.Store
	access        *accsql.Store
	notifications *ntfsql.Store
	retention     *retsql.Store
	federation    *fedsql.Store
	releases      *relsql.Store
	workers       *wrksql.Store
}

var _ store.Store = (*Store)(nil)

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
func Open(ctx context.Context, db *sql.DB, d Dialect, o Options) (*Store, error) {
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	mods := Modules()
	if !o.SkipMigrate {
		if _, err := Migrate(ctx, db, d, mods, ""); err != nil {
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

// New wraps an open, migrated pool. It registers the layout fence with the
// platform's config store: a layout document that is created, changed or
// deleted fences the remote repositories that use it, in the same
// transaction (decision 0007).
func New(db *sql.DB, d Dialect, keys *crypt.Keyring) *Store {
	s := &Store{Store: dialect.New(db, d, keys)}
	s.platform = platsql.New(s.Store, configdoc.Watch{
		Kind: configdoc.ConfigLayout,
		Changed: func(ctx context.Context, key string) error {
			return store.FenceLayoutSources(ctx, s, remoteRecords{s}, key)
		},
	})
	s.access = accsql.New(s.Store)
	s.notifications = ntfsql.New(s.Store)
	s.retention = retsql.New(s.Store)
	s.federation = fedsql.New(s.Store)
	s.releases = relsql.New(s.Store)
	s.workers = wrksql.New(s.Store)
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

// Users implements store.Store with the access module's user store.
func (s *Store) Users() store.UserStore { return s.access.Users() }

// Groups implements store.Store with the access module's group store.
func (s *Store) Groups() store.GroupStore { return s.access.Groups() }

// Permissions implements store.Store with the access module's permission
// store.
func (s *Store) Permissions() store.PermissionStore { return s.access.Permissions() }

// Tokens implements store.Store with the access module's token store.
func (s *Store) Tokens() store.TokenStore { return s.access.Tokens() }

// Keys implements store.Store with the access module's key store.
func (s *Store) Keys() store.KeyStore { return s.access.Keys() }

// Projects implements store.Store with the access module's project store.
func (s *Store) Projects() store.ProjectStore { return s.access.Projects() }

// APIRestrictions implements store.Store with the access module's API
// restriction store.
func (s *Store) APIRestrictions() store.APIRestrictionStore { return s.access.APIRestrictions() }

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
