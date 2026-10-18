package store

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RepoFilter narrows repository listings; empty fields match everything.
type RepoFilter struct {
	Class       repo.Class
	PackageType repo.PackageType
	ProjectKey  string
}

// RepoStore persists repository configurations. The remote password is
// sealed in its own column; the virtual member list and the key pair
// references are mirrored into tables so Including and UsingKeypair are
// indexed lookups.
type RepoStore interface {
	// Create stores c; ErrConflict when the key exists.
	Create(ctx context.Context, c repo.Config) error
	// Get returns the configuration, password included.
	Get(ctx context.Context, key string) (repo.Config, error)
	// Update replaces the stored configuration; ErrNotFound when absent.
	Update(ctx context.Context, c repo.Config) error
	// Delete removes the configuration only. Content is removed through
	// ItemStore.Delete and the package index through PackageStore.
	Delete(ctx context.Context, key string) error
	// List returns every matching repository ordered by key. Artifactory
	// lists repositories unpaged, so this does too.
	List(ctx context.Context, f RepoFilter) ([]repo.Config, error)
	// Including returns the keys of the virtual repositories that list key
	// as a member, ordered.
	Including(ctx context.Context, key string) ([]string, error)
	// UsingKeypair returns the keys of repositories whose primary or
	// secondary key pair is name, ordered.
	UsingKeypair(ctx context.Context, name string) ([]string, error)
}

// Download is one recorded download of an item. Remote marks a download
// served to another instance through a smart remote repository, which
// Artifactory counts separately along with where it went.
type Download struct {
	By     string
	At     time.Time
	Remote bool
	Origin string
	Path   string
}

// DeleteResult summarises an ItemStore.Delete. BlobRefs counts, per sha256,
// the file references removed, which is what the caller passes to
// BlobIndex.Unref.
type DeleteResult struct {
	Files    int64
	Folders  int64
	Bytes    int64
	BlobRefs map[string]int64
}

// RepoUsage is one repository's (or one remote cache's) content totals for
// the storage summary.
type RepoUsage struct {
	RepoKey string
	Cached  bool
	Files   int64
	Folders int64
	Bytes   int64
}

// ItemStore persists items in the canonical form of decision 0004, with
// their properties and download statistics. An item's natural key is
// (RepoKey, Cached, Path, Name); its ID is assigned by the store. Paths are
// repository-relative without a leading slash; "" and "." both name the
// repository root.
//
// A repository root has a record (types.RootItem) once properties are set
// on it, so they are kept and found by item ID like any item's. Only Root,
// EnsureRoot, GetByID and Search return it; Get, Children, Descendants,
// FindByChecksum and Usage leave it out, as does the result of Delete.
type ItemStore interface {
	// Put creates the item or replaces the one at the same natural key,
	// keeping that item's ID, and returns it as stored. The store derives
	// Path, Name and Depth from FullPath, so callers may set either Path and
	// Name or a full path in Name; ErrInvalid when it does not split
	// (types.SplitItemPath). Parent folders are not created.
	Put(ctx context.Context, it types.Item) (types.Item, error)
	// Get returns the item at fullPath.
	Get(ctx context.Context, repoKey string, cached bool, fullPath string) (types.Item, error)
	// GetByID returns the item with id.
	GetByID(ctx context.Context, id int64) (types.Item, error)
	// Children lists the items directly inside folder, ordered by name.
	Children(
		ctx context.Context, repoKey string, cached bool, folder string, p Page,
	) ([]types.Item, string, error)
	// Descendants lists every item below folder, ordered by (path, name).
	Descendants(
		ctx context.Context, repoKey string, cached bool, folder string, p Page,
	) ([]types.Item, string, error)
	// Delete removes the item at fullPath and, for a folder, everything
	// below it, with their properties and statistics. A root path removes
	// all of (repoKey, cached) but the root's record, which belongs to the
	// repository rather than its content, and returns a zero result for an
	// empty repository; any other path returns ErrNotFound when nothing is
	// there.
	Delete(ctx context.Context, repoKey string, cached bool, fullPath string) (DeleteResult, error)
	// Root returns the record of the root of (repoKey, cached), ErrNotFound
	// while it has none.
	Root(ctx context.Context, repoKey string, cached bool) (types.Item, error)
	// EnsureRoot returns the record of the root of (r.RepoKey, r.Cached),
	// creating it as types.RootItem with r's times and actors when absent;
	// ErrInvalid without a repository key.
	EnsureRoot(ctx context.Context, r types.Item) (types.Item, error)
	// DeleteRoot removes the record of the root of (repoKey, cached) with
	// its properties, when the repository itself is removed. A root without
	// a record is not an error.
	DeleteRoot(ctx context.Context, repoKey string, cached bool) error
	// FindByChecksum lists files whose actual checksum matches the
	// strongest digest set in c (sha256, then sha1, then md5), ordered by
	// ID. SHA512 is not a lookup key: whatever holds one holds the sha256.
	FindByChecksum(ctx context.Context, c types.Checksums, p Page) ([]types.Item, string, error)
	// Usage returns content totals per repository and cache, ordered by
	// (repository, cached).
	Usage(ctx context.Context) ([]RepoUsage, error)
	// Search runs an AQL items query: the matching items in the query's
	// order (then by ID), after its offset and within its limit, or within
	// o.Limit when it has none. Hits carry their properties only when the
	// query includes them. Every backend returns the same rows in the same
	// order as aql.Select.
	Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error)

	// Properties returns the item's properties (empty when it has none).
	// This and the other property and statistics methods return ErrNotFound
	// for a missing item. A key given with no values is not stored.
	Properties(ctx context.Context, id int64) (types.Properties, error)
	// SetProperties replaces all of the item's properties.
	SetProperties(ctx context.Context, id int64, props types.Properties) error
	// PatchProperties removes the keys in remove, then replaces the values of
	// the keys in set (so a key in both ends with the set values), leaving
	// other keys alone.
	PatchProperties(ctx context.Context, id int64, set types.Properties, remove []string) error

	// Stats returns the item's statistics, zero when nothing was recorded.
	Stats(ctx context.Context, id int64) (types.ItemStats, error)
	// RecordDownload counts one download.
	RecordDownload(ctx context.Context, id int64, d Download) error
	// PutStats replaces the statistics, for import and federation sync.
	PutStats(ctx context.Context, id int64, s types.ItemStats) error
}

// BlobIndex is the content-addressed blob index (the blobs table, and the
// cold_blobs table for the archive tier). Rows are keyed by sha256 and
// reference-counted; garbage collection deletes blobs whose count reached
// zero.
//
// Garbage collection removes a blob in three steps (decision 0010):
// MarkDeleting, then the content delete, then FinishDelete. A deleting row
// refuses references, and the tombstone FinishDelete leaves records when the
// content went, so a deploy that wrote the same content just before it was
// removed finds out and never references missing content.
type BlobIndex interface {
	// Ref adds n references to b and returns the row as updated. An absent
	// row is created live; a tombstone is revived, keeping Deleted.
	// ErrDeleting while the row is deleting; ErrConflict when the stored size
	// or checksums differ from b's, which means two different contents claim
	// one sha256.
	Ref(ctx context.Context, b types.Blob, n int64) (types.Blob, error)
	// Unref removes n references, never going below zero, and returns the
	// row as updated.
	Unref(ctx context.Context, sha256 string, n int64) (types.Blob, error)
	// Get returns the row for sha256, in whatever state.
	Get(ctx context.Context, sha256 string) (types.Blob, error)
	// Find returns the live blob matching the strongest digest set in c
	// (sha256, then sha1, then md5). SHA512 is not a lookup key, as for
	// FindByChecksum.
	Find(ctx context.Context, c types.Checksums) (types.Blob, error)
	// Unreferenced lists live blobs with no references, ordered by sha256.
	Unreferenced(ctx context.Context, p Page) ([]types.Blob, string, error)
	// MarkDeleting starts removing b's content: an unreferenced live row or a
	// tombstone becomes deleting at at, and content the index has no row for
	// gets a deleting row from b. A row already deleting is returned as it
	// is. ErrConflict while the row is referenced.
	MarkDeleting(ctx context.Context, b types.Blob, at time.Time) (types.Blob, error)
	// FinishDelete turns a deleting row into a tombstone deleted at at, once
	// its content is gone. A tombstone is left as it is; ErrConflict for a
	// live row, ErrNotFound when there is no row.
	FinishDelete(ctx context.Context, sha256 string, at time.Time) error
	// Deleting lists the rows being deleted, ordered by sha256, so a pass
	// can finish what a crashed one started.
	Deleting(ctx context.Context, p Page) ([]types.Blob, string, error)
	// Prune drops tombstones deleted before before and clears Deleted on
	// live rows deleted before then, returning how many rows changed.
	Prune(ctx context.Context, before time.Time) (int64, error)
	// Totals returns the number of live blobs and their combined size.
	Totals(ctx context.Context) (count, bytes int64, err error)
}

// ReplicaFilter narrows replica listings; empty fields match everything.
type ReplicaFilter struct {
	Member string
	States []types.ReplicaState
}

// ReplicaCount is one member's number of replicas in one state and their
// combined size, for the storage summary.
type ReplicaCount struct {
	Member string
	State  types.ReplicaState
	Count  int64
	Bytes  int64
}

// ReplicaIndex records which mirror members hold which blobs (decision 0028):
// the blob_replicas table, and cold_blob_replicas for the archive tier.
type ReplicaIndex interface {
	// Put creates or replaces the record (r.SHA256, r.Member); ErrInvalid
	// without both.
	Put(ctx context.Context, r types.BlobReplica) error
	// Get returns one record.
	Get(ctx context.Context, sha256, member string) (types.BlobReplica, error)
	// ForBlob returns a blob's records ordered by member.
	ForBlob(ctx context.Context, sha256 string) ([]types.BlobReplica, error)
	// List returns matching records oldest update first, by (Updated, SHA256,
	// Member), which is the order the repair worker drains them in.
	List(ctx context.Context, f ReplicaFilter, p Page) ([]types.BlobReplica, string, error)
	// Delete removes one record.
	Delete(ctx context.Context, sha256, member string) error
	// DeleteBlob removes every record of a blob and returns how many.
	DeleteBlob(ctx context.Context, sha256 string) (int64, error)
	// Counts returns the number and size of records per member and state,
	// ordered by (member, state).
	Counts(ctx context.Context) ([]ReplicaCount, error)
	// UnderReplicated counts the blobs with fewer than redundancy present
	// records. A blob with a deleting record is being deleted and is not
	// counted; neither is a blob with no records at all, which the repair
	// worker's backfill brings into the index.
	UnderReplicated(ctx context.Context, redundancy int) (int64, error)
	// SoleCopies counts, per member, the blobs whose only present record is
	// on that member. Blobs being deleted are left out, as UnderReplicated
	// leaves them out. A member holding sole copies cannot be removed from
	// a mirror without losing them: repair never reads from a member that
	// is no longer configured.
	SoleCopies(ctx context.Context) (map[string]int64, error)
}

// Upload is a blob upload session that outlives one request: a Docker
// chunked upload, the CLI's multipart upload, or an S3 multipart session.
// State is the blob backend's own session record, opaque here.
type Upload struct {
	ID      string
	Backend string
	RepoKey string
	Path    string
	State   []byte
	Bytes   int64
	Started time.Time
	Updated time.Time
}

// UploadStore persists upload sessions.
type UploadStore interface {
	// Put creates or replaces the session with u.ID.
	Put(ctx context.Context, u Upload) error
	// Get returns the session.
	Get(ctx context.Context, id string) (Upload, error)
	// Delete removes the session.
	Delete(ctx context.Context, id string) error
	// Stale lists sessions last updated (or started, when never updated)
	// before t, oldest first, at most limit (DefaultLimit when zero), for
	// the reaper.
	Stale(ctx context.Context, before time.Time, limit int) ([]Upload, error)
}

// TrashStore persists the trash can.
type TrashStore interface {
	// Add stores e and returns it with its assigned ID.
	Add(ctx context.Context, e types.TrashEntry) (types.TrashEntry, error)
	// Get returns the entry with id.
	Get(ctx context.Context, id int64) (types.TrashEntry, error)
	// Find returns the entries for an original repository and full path,
	// newest first (entries without a deletion time last). A path that does
	// not split into parent and name matches nothing.
	Find(ctx context.Context, repoKey, fullPath string) ([]types.TrashEntry, error)
	// List returns entries from repoKey ("" for every repository) whose
	// original full path is prefix or below it ("" for all), ordered by
	// (repository, path, name, ID).
	List(ctx context.Context, repoKey, prefix string, p Page) ([]types.TrashEntry, string, error)
	// Delete removes an entry.
	Delete(ctx context.Context, id int64) error
	// Expired returns entries deleted before t, oldest first, at most limit
	// (DefaultLimit when zero).
	Expired(ctx context.Context, before time.Time, limit int) ([]types.TrashEntry, error)
}

// PackageStore persists the package index of decision 0018.
type PackageStore interface {
	// Ensure returns the package (p.RepoKey, p.Name), creating it from p
	// when absent.
	Ensure(ctx context.Context, p types.Package) (types.Package, error)
	// Get returns the package with id.
	Get(ctx context.Context, id int64) (types.Package, error)
	// Find returns the package called name in repoKey.
	Find(ctx context.Context, repoKey, name string) (types.Package, error)
	// List returns a repository's packages ordered by name.
	List(ctx context.Context, repoKey string, p Page) ([]types.Package, string, error)
	// Delete removes the package with its versions and file links.
	Delete(ctx context.Context, id int64) error

	// PutVersion creates or replaces version (v.PackageID, v.Version), then
	// recomputes OrderKey for every version of the package with the
	// package type's comparator (types.ComparatorFor), and returns v as
	// stored.
	PutVersion(ctx context.Context, v types.PackageVersion) (types.PackageVersion, error)
	// Version returns one version of a package.
	Version(ctx context.Context, packageID int64, version string) (types.PackageVersion, error)
	// Versions returns a package's versions ordered by OrderKey, then
	// version string.
	Versions(ctx context.Context, packageID int64) ([]types.PackageVersion, error)
	// DeleteVersion removes a version and its file links and recomputes the
	// order keys of the rest.
	DeleteVersion(ctx context.Context, id int64) error
	// LinkFile records that an item belongs to a version; linking twice is
	// not an error. ErrNotFound when the version or the item is missing.
	LinkFile(ctx context.Context, f types.VersionFile) error
	// Files returns the item IDs of a version, ordered.
	Files(ctx context.Context, versionID int64) ([]int64, error)
	// VersionOf returns the version an item belongs to, the one with the
	// lowest ID when several link it.
	VersionOf(ctx context.Context, itemID int64) (types.PackageVersion, error)
	// RecordVersionDownload sets the version's LastDownloaded.
	RecordVersionDownload(ctx context.Context, versionID int64, at time.Time) error
}
