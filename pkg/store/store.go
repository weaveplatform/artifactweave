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
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Implementations wrap them; callers test with errors.Is.
var (
	// ErrNotFound reports that a keyed record does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict reports a create that collides with an existing key, or a
	// change that would break a uniqueness rule.
	ErrConflict = errors.New("store: conflict")
	// ErrDeleting reports a blob whose content garbage collection is
	// removing: it cannot be referenced until the removal has finished.
	ErrDeleting = errors.New("store: blob is being deleted")
	// ErrInvalidCursor reports a malformed pagination cursor.
	ErrInvalidCursor = errors.New("store: invalid cursor")
	// ErrInvalid reports a record the store cannot hold as given, for
	// example an item path that does not split into parent and name.
	ErrInvalid = errors.New("store: invalid record")
)

// Page bounds.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// Page selects one page of a list. After is the next value returned with the
// previous page ("" for the first page). Limit bounds the page: DefaultLimit
// when zero or negative, capped at MaxLimit.
type Page struct {
	After string
	Limit int
}

// cursorSep separates the parts of a compound sort key. Unit separator: no
// key the stores sort by can contain it.
const cursorSep = "\x1f"

// Cursor returns the opaque cursor that continues a list after the record
// whose sort key is parts. Implementations call it for the last record of a
// page; callers treat the result as opaque.
func Cursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, cursorSep)))
}

// Normalize applies the default and maximum limit and checks that After
// decodes.
func (p Page) Normalize() (Page, error) {
	if p.After != "" {
		if _, err := base64.RawURLEncoding.DecodeString(p.After); err != nil {
			return p, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
		}
	}
	switch {
	case p.Limit <= 0:
		p.Limit = DefaultLimit
	case p.Limit > MaxLimit:
		p.Limit = MaxLimit
	}
	return p, nil
}

// Key decodes After into the n parts of the sort key it continues after. It
// returns nil for the first page and ErrInvalidCursor when After does not
// carry exactly n parts, which is what a cursor from a different list looks
// like.
func (p Page) Key(n int) ([]string, error) {
	if p.After == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.After)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	parts := strings.Split(string(raw), cursorSep)
	if len(parts) != n {
		return nil, fmt.Errorf("%w: want %d parts, got %d", ErrInvalidCursor, n, len(parts))
	}
	return parts, nil
}

// Trim cuts rows, fetched with one more row than limit, to the page and
// returns the cursor of the next page ("" when rows was the last page). key
// returns a record's sort key, the parts Page.Key decodes.
func Trim[T any](rows []T, limit int, key func(T) []string) ([]T, string) {
	if len(rows) <= limit {
		return rows, ""
	}
	rows = rows[:limit]
	return rows, Cursor(key(rows[limit-1])...)
}

// Store is the complete metadata persistence surface.
type Store interface {
	Repos() RepoStore
	Items() ItemStore
	Blobs() BlobIndex
	Replicas() ReplicaIndex
	Uploads() UploadStore
	Trash() TrashStore
	Packages() PackageStore

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
