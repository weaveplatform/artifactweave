package types

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// ErrInvalidItemPath reports a repository path that cannot name an item.
var ErrInvalidItemPath = errors.New("invalid item path")

// ErrInvalidPropertyKey reports a property key that the matrix-parameter and
// properties-query syntaxes could not carry.
var ErrInvalidPropertyKey = errors.New("invalid property key")

// RootPath is the parent path of an item that sits at the repository root.
// AQL reports it as "." and the item table stores it the same way, so a root
// listing and a nested listing are the same query (decision 0004).
const RootPath = "."

// CacheSuffix marks a remote repository's cache in item and AQL responses.
const CacheSuffix = "-cache"

// Checksums are hex digests of an item's content.
type Checksums struct {
	SHA256 string `json:"sha256,omitempty"`
	SHA512 string `json:"sha512,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	MD5    string `json:"md5,omitempty"`
}

// Item is one file or folder in the canonical form of decision 0004. Path is
// the parent folder ("." at the root), Name the last segment, and Depth the
// number of segments in the full path, so an item at the root has depth 1.
// Cached items belong to a remote repository's cache and are reported under
// RepoKey plus CacheSuffix. Actual holds what the server computed; Original
// holds what the client declared at deploy time, so a mismatch stays
// visible to the bad-checksum search. The type is persisted as part of the
// trash record, hence the tags.
type Item struct {
	ID         int64     `json:"id"`
	RepoKey    string    `json:"repo_key"`
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Depth      int       `json:"depth"`
	Folder     bool      `json:"folder"`
	Cached     bool      `json:"cached"`
	Size       int64     `json:"size"`
	Actual     Checksums `json:"actual"`
	Original   Checksums `json:"original"`
	MimeType   string    `json:"mime_type,omitempty"`
	Created    time.Time `json:"created"`
	CreatedBy  string    `json:"created_by,omitempty"`
	Modified   time.Time `json:"modified"`
	ModifiedBy string    `json:"modified_by,omitempty"`
	Updated    time.Time `json:"updated"`
	RemoteURL  string    `json:"remote_url,omitempty"`
}

// FullPath is the item's path inside its repository without a leading slash.
func (i Item) FullPath() string {
	if i.Path == RootPath || i.Path == "" {
		return i.Name
	}
	return i.Path + "/" + i.Name
}

// StoragePath is the path as the storage API prints it, with a leading slash.
func (i Item) StoragePath() string { return "/" + i.FullPath() }

// ReportedRepoKey is the repository key clients see for the item: the cache
// alias for cached items (decision 0007).
func (i Item) ReportedRepoKey() string {
	if i.Cached {
		return i.RepoKey + CacheSuffix
	}
	return i.RepoKey
}

// SplitItemPath turns a repository-relative path into the canonical parent,
// name and depth. Leading and trailing slashes are ignored, so folder paths
// from deploys ("a/b/") and storage paths ("/a/b") both work; empty, "." and
// ".." segments are refused because they would let a path escape or alias
// another item.
func SplitItemPath(full string) (parent, name string, depth int, err error) {
	full = strings.Trim(full, "/")
	if full == "" {
		return "", "", 0, fmt.Errorf("%w: empty", ErrInvalidItemPath)
	}
	segments := strings.Split(full, "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00") {
			return "", "", 0, fmt.Errorf("%w: %q", ErrInvalidItemPath, full)
		}
	}
	name = segments[len(segments)-1]
	parent = RootPath
	if len(segments) > 1 {
		parent = strings.Join(segments[:len(segments)-1], "/")
	}
	return parent, name, len(segments), nil
}

// ResolveRepoKey maps a key as a client sent it to the stored key and the
// cached flag: "maven-remote-cache" is the cache of "maven-remote". Repository
// keys cannot end in the suffix (pkg/repo.Validate), so the mapping is
// unambiguous.
func ResolveRepoKey(key string) (stored string, cached bool) {
	if base, ok := strings.CutSuffix(key, CacheSuffix); ok && base != "" {
		return base, true
	}
	return key, false
}

// Properties are an item's multi-valued properties. Values are kept sorted
// and unique so listings and comparisons are deterministic.
type Properties map[string][]string

// ValidatePropertyKey refuses keys that the ";key=value" matrix syntax and
// the "?properties=k=v|k2=v2" query syntax could not carry unambiguously.
func ValidatePropertyKey(key string) error {
	if key == "" || strings.ContainsAny(key, "=;,|/\\\"") ||
		strings.ContainsFunc(key, isSpaceOrControl) {
		return fmt.Errorf("%w: %q", ErrInvalidPropertyKey, key)
	}
	return nil
}

func isSpaceOrControl(r rune) bool { return r <= ' ' || r == 0x7f }

// Set replaces the values of key.
func (p Properties) Set(key string, values ...string) {
	p[key] = normaliseValues(values)
}

// Add adds values to key, keeping existing ones.
func (p Properties) Add(key string, values ...string) {
	p[key] = normaliseValues(append(slices.Clone(p[key]), values...))
}

// Has reports whether key carries value; an empty value matches any value,
// which is how property search treats a key given without a value.
func (p Properties) Has(key, value string) bool {
	values, ok := p[key]
	if !ok {
		return false
	}
	return value == "" || slices.Contains(values, value)
}

// Keys returns the keys in sorted order.
func (p Properties) Keys() []string { return slices.Sorted(maps.Keys(p)) }

// Clone returns a deep copy.
func (p Properties) Clone() Properties {
	out := make(Properties, len(p))
	for k, v := range p {
		out[k] = slices.Clone(v)
	}
	return out
}

func normaliseValues(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

// ItemStats are an item's download statistics. They change out of band of the
// item record (every download updates them), so they live in their own table.
type ItemStats struct {
	DownloadCount          int64     `json:"download_count"`
	LastDownloaded         time.Time `json:"last_downloaded"`
	LastDownloadedBy       string    `json:"last_downloaded_by,omitempty"`
	RemoteDownloadCount    int64     `json:"remote_download_count"`
	RemoteLastDownloaded   time.Time `json:"remote_last_downloaded"`
	RemoteLastDownloadedBy string    `json:"remote_last_downloaded_by,omitempty"`
	RemoteOrigin           string    `json:"remote_origin,omitempty"`
	RemotePath             string    `json:"remote_path,omitempty"`
}

// TrashRecord is what the trash can keeps of a deleted item, as one JSON
// document, so a restore recreates the item and its properties exactly.
type TrashRecord struct {
	Item       Item       `json:"item"`
	Properties Properties `json:"properties,omitempty"`
}

// TrashEntry is one deleted item awaiting restore or purge.
type TrashEntry struct {
	ID        int64
	Record    TrashRecord
	DeletedAt time.Time
	DeletedBy string
}

// Expired reports whether the entry is older than the retention period.
func (t TrashEntry) Expired(now time.Time, retention time.Duration) bool {
	return !now.Before(t.DeletedAt.Add(retention))
}

// Blob is the index record of one stored content blob. Items reference blobs
// by SHA256; Refs counts those references (and those of trash entries and
// bundle artifacts) so garbage collection removes a blob only when nothing
// can serve it again. SHA512, SHA1 and MD5 are recorded so checksum deploy,
// the checksum searches and checksum files resolve without reading content.
type Blob struct {
	Checksums Checksums
	Size      int64
	Created   time.Time
	Refs      int64
	// State is where the row stands in garbage collection (decision 0010);
	// only live rows serve content.
	State BlobState
	// Deleted is when garbage collection marked the content for deletion
	// (deleting) or removed it (deleted). A row revived by a deploy keeps the
	// time until it is pruned, so the deploy knows the content it found may
	// have been removed after it was written and checks it is still there.
	Deleted time.Time
}

// BlobState is a blob index row's stage in garbage collection. A row is
// marked deleting before its content is removed and becomes deleted (a
// tombstone) once it has been, so a deploy of the same content can never
// reference content that is being removed (decision 0010).
type BlobState string

// Blob index row states.
const (
	BlobLive     BlobState = "live"
	BlobDeleting BlobState = "deleting"
	BlobDeleted  BlobState = "deleted"
)
