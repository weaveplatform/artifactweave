package store

import (
	"context"
	"time"

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
	// all of (repoKey, cached) and returns a zero result for an empty
	// repository; any other path returns ErrNotFound when nothing is there.
	Delete(ctx context.Context, repoKey string, cached bool, fullPath string) (DeleteResult, error)
	// FindByChecksum lists files whose actual checksum matches the
	// strongest digest set in c (sha256, then sha1, then md5), ordered by
	// ID.
	FindByChecksum(ctx context.Context, c types.Checksums, p Page) ([]types.Item, string, error)
	// Usage returns content totals per repository and cache, ordered by
	// (repository, cached).
	Usage(ctx context.Context) ([]RepoUsage, error)

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
type BlobIndex interface {
	// Ref adds n references to b, creating its row when absent. ErrConflict
	// when the stored size or checksums differ from b's, which means two
	// different contents claim one sha256.
	Ref(ctx context.Context, b types.Blob, n int64) (types.Blob, error)
	// Unref removes n references, never going below zero, and returns the
	// row as updated.
	Unref(ctx context.Context, sha256 string, n int64) (types.Blob, error)
	// Get returns the row for sha256.
	Get(ctx context.Context, sha256 string) (types.Blob, error)
	// Find returns the blob matching the strongest digest set in c (sha256,
	// then sha1, then md5).
	Find(ctx context.Context, c types.Checksums) (types.Blob, error)
	// Unreferenced lists blobs with no references, ordered by sha256.
	Unreferenced(ctx context.Context, p Page) ([]types.Blob, string, error)
	// Delete removes an unreferenced row; ErrConflict while referenced.
	Delete(ctx context.Context, sha256 string) error
	// Totals returns the number of blobs and their combined size.
	Totals(ctx context.Context) (count, bytes int64, err error)
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
