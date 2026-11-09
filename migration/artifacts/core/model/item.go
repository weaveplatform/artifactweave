package model

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// ErrInvalidPropertyKey reports a property key that the matrix-parameter and
// properties-query syntaxes could not carry.
var ErrInvalidPropertyKey = errors.New("invalid property key")

// CacheSuffix marks a remote repository's cache in item and AQL responses.
const CacheSuffix = "-cache"

// Item is one file or folder in the canonical form of decision 0004. Path is
// the parent folder ("." at the root), Name the last segment, and Depth the
// number of segments in the full path, so an item at the root has depth 1.
// Cached items belong to a remote repository's cache and are reported under
// RepoKey plus CacheSuffix. Actual holds what the server computed; Original
// holds what the client declared at deploy time, so a mismatch stays
// visible to the bad-checksum search. The type is persisted as part of the
// trash record, hence the tags.
//
// PropertiesModified is when the item's properties last changed, zero while
// it has none; the storage API lists it as mdTimestamps.properties. Only
// the property methods of port.ItemStore set it. Put keeps the stored value
// whatever the caller passes, so a trash record or a copied item cannot
// carry another item's time onto the one written.
type Item struct {
	ID         int64            `json:"id"`
	RepoKey    string           `json:"repo_key"`
	Path       string           `json:"path"`
	Name       string           `json:"name"`
	Depth      int              `json:"depth"`
	Folder     bool             `json:"folder"`
	Cached     bool             `json:"cached"`
	Size       int64            `json:"size"`
	Actual     kernel.Checksums `json:"actual"`
	Original   kernel.Checksums `json:"original"`
	MimeType   string           `json:"mime_type,omitempty"`
	Created    time.Time        `json:"created"`
	CreatedBy  string           `json:"created_by,omitempty"`
	Modified   time.Time        `json:"modified"`
	ModifiedBy string           `json:"modified_by,omitempty"`
	Updated    time.Time        `json:"updated"`
	RemoteURL  string           `json:"remote_url,omitempty"`

	PropertiesModified time.Time `json:"properties_modified,omitzero"`
}

// RootItem is the record of a repository root, which holds the properties
// set on the repository itself. AQL reports a root with "." as both its path
// and its name and a depth of 0, so the record is stored that way; no other
// item can be named ".", because SplitItemPath refuses the segment.
func RootItem(repoKey string, cached bool) Item {
	return Item{
		RepoKey: repoKey,
		Cached:  cached,
		Path:    kernel.RootPath,
		Name:    kernel.RootPath,
		Folder:  true,
	}
}

// IsRoot reports whether i is a repository root's record (RootItem).
func (i Item) IsRoot() bool { return i.Path == kernel.RootPath && i.Name == kernel.RootPath }

// FullPath is the item's path inside its repository without a leading slash,
// "" for a repository root.
func (i Item) FullPath() string {
	switch {
	case i.IsRoot():
		return ""
	case i.Path == kernel.RootPath || i.Path == "":
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

// ResolveRepoKey maps a key as a client sent it to the stored key and the
// cached flag: "maven-remote-cache" is the cache of "maven-remote". Repository
// keys cannot end in the suffix (migration/artifacts/api/repo.Validate), so the
// mapping is unambiguous.
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
// the "?properties=k=v|k2=v2" query syntax could not carry unambiguously,
// and keys CheckPropertyKeyLength refuses.
func ValidatePropertyKey(key string) error {
	if key == "" || strings.ContainsAny(key, "=;,|/\\\"") ||
		strings.ContainsFunc(key, isSpaceOrControl) {
		return fmt.Errorf("%w: %q", ErrInvalidPropertyKey, key)
	}
	return CheckPropertyKeyLength(key)
}

func isSpaceOrControl(r rune) bool { return r <= ' ' || r == 0x7f }

// PropertyKeyMax is the longest property key, in characters: the limit
// JFrog's 7.x documentation gives ("JFrog Properties"), and the width of
// Artifactory's VARCHAR(255) key columns. Artifactory checks nothing before
// its database refuses a longer key (RTFACT-21394); artifactweave answers
// 400 (DV-059).
const PropertyKeyMax = 255

// ErrPropertyKeyTooLong reports a key longer than PropertyKeyMax. It wraps
// ErrInvalidPropertyKey.
var ErrPropertyKeyTooLong = fmt.Errorf("%w: longer than %d characters",
	ErrInvalidPropertyKey, PropertyKeyMax)

// CheckPropertyKeyLength returns ErrPropertyKeyTooLong for a key longer
// than PropertyKeyMax characters. The stores check it too: on PostgreSQL a
// key is indexed whole beside a value's prefix, and a much longer key would
// not fit a B-tree entry.
func CheckPropertyKeyLength(key string) error {
	if utf8.RuneCountInString(key) > PropertyKeyMax {
		return ErrPropertyKeyTooLong
	}
	return nil
}

// PropertyValueMax and BuildPropertyValueMax are the longest property values
// the stores keep, in UTF-16 code units, as Artifactory's Java counts them.
// JFrog's 7.x documentation limits property values to 2400 characters ("JFrog
// Properties"); build and module properties keep 6.20's 2048 (BuildsDao and
// BuildModulesDao), which the documentation does not cover. Artifactory 6.20
// cuts a longer value on write rather than refusing it (PropertiesDao), and
// so do the stores.
const (
	PropertyValueMax      = 2400
	BuildPropertyValueMax = 2048
)

// TruncatePropertyValue cuts v to at most limit UTF-16 code units. Java's
// substring, which Artifactory uses, can split a surrogate pair; here a
// character that would straddle the limit is dropped whole, so the result
// stays valid UTF-8.
func TruncatePropertyValue(v string, limit int) string {
	// A UTF-8 string has at least as many bytes as UTF-16 code units.
	if len(v) <= limit {
		return v
	}
	units := 0
	for i, r := range v {
		units += utf16.RuneLen(r)
		if units > limit {
			return v[:i]
		}
	}
	return v
}

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

// Equal reports whether p and o hold the same keys with the same values in
// the same order; nil and empty are equal. Values in the order Set keeps
// them compare as sets.
func (p Properties) Equal(o Properties) bool { return maps.EqualFunc(p, o, slices.Equal[[]string]) }

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
	// PackageType is the package type of the repository the item was
	// deleted from, recorded then because the trash can still shows it once
	// that repository is gone, when it tells an administrator what type of
	// repository to recreate for a restore.
	PackageType string `json:"packageType,omitempty"` //nolint:tagliatelle // stored in trash records
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
