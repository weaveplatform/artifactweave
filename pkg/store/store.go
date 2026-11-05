// Package store defines artifactweave's metadata persistence contracts
// (decision 0004) over the domain types of pkg/types and pkg/repo.
//
// # Design
//
// Store composes one interface per concern so services and fakes depend only
// on what they use. Every method takes a context. Tx runs a function whose
// context makes every store call inside it part of one transaction; that is
// how a deploy writes the item, its properties, the blob reference and the
// outbox entry for federation together, or not at all (decision 0021).
// Returned values are copies: mutating one never changes stored state.
//
// Records are keyed by their wire identities (repository key, user name,
// token id, the (name, version) of a release bundle). Records without one
// (items, runs, executions, deliveries) get an int64 identifier from the
// store, increasing in creation order.
//
// Lists are keyset-paginated. A Page carries an opaque cursor from the
// previous call's next value; a list returns next == "" on its last page.
// Each list documents its order, which is stable under concurrent inserts.
//
// Times are stored in UTC at microsecond precision and durations at
// millisecond precision, which is what the SQL columns hold; a zero time
// means "not set" and comes back zero. Every backend normalises on write, so
// a value read back equals the value written once normalised.
//
// Sealed fields (decision 0017) are given and returned in plain text; the SQL
// stores seal them with the configured keyring and keep them out of every
// JSON document column, so no listing or document read can leak one.
//
// pkg/store/inmem is the reference implementation, internal/sqlstore
// implements the same contract for SQLite and PostgreSQL, and storetest is
// the contract suite every backend runs.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//   - Research 15 section 4: docs/research/15-data-model.md
package store

import (
	"context"
	"errors"
)

// ErrDeleting reports a blob whose content garbage collection is
// removing: it cannot be referenced until the removal has finished.
var ErrDeleting = errors.New("store: blob is being deleted")

// Store is the complete metadata persistence surface.
type Store interface {
	Repos() RepoStore
	RemoteCache() RemoteCacheStore
	Items() ItemStore
	Blobs() BlobIndex
	Replicas() ReplicaIndex
	Uploads() UploadStore
	Trash() TrashStore
	Packages() PackageStore
	GitLFSLocks() GitLFSLockStore
	BowerRegistrations() BowerRegistrationStore

	Users() UserStore
	Groups() GroupStore
	Permissions() PermissionStore
	Tokens() TokenStore
	Keys() KeyStore

	Projects() ProjectStore
	Webhooks() WebhookStore
	Cleanup() CleanupStore
	Replications() ReplicationStore
	Settings() SettingsStore
	Leases() LeaseStore
	WorkQueue() WorkQueue
	Replays() ReplayStore
	RequestBudgets() RequestBudgetStore
	APIRestrictions() APIRestrictionStore
	Configs() ConfigStore

	Instance() InstanceStore
	Peers() PeerStore
	Outbox() OutboxStore
	Federation() FederationStore
	Platform() PlatformStore

	Bundles() BundleStore
	Distribution() DistributionStore
	Archive() ArchiveStore
	Workers() WorkerStore

	Builds() BuildStore

	// Tx runs fn in one transaction. Store calls made with the context fn
	// receives take part; an error from fn rolls all of them back. A nested
	// Tx joins the outer transaction.
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
	// AfterCommit defers fn until the enclosing transaction commits, or runs
	// it at once outside a transaction. Nothing runs after a rollback.
	AfterCommit(ctx context.Context, fn func())
	// Close releases resources.
	Close() error
}
