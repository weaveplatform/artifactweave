package port

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// RemoteCacheStore persists raw upstream representations and their CAS fences.
// All writes join Store.Tx. Begin precedes network I/O; Check/Put run in the
// publication transaction. kernel.ErrConflict means the caller must start a new
// read.
type RemoteCacheStore interface {
	Source(ctx context.Context, repoKey string) (model.SourceIdentity, error)
	Begin(ctx context.Context, key model.RemoteCacheKey) (model.RemoteCacheToken, error)
	Read(ctx context.Context, key model.RemoteCacheKey) (model.RemoteCacheEntry, error)
	// List includes raw metadata, negatives and tombstones, ordered by opaque
	// key.
	List(
		ctx context.Context,
		repoKey, prefix string,
		p kernel.Page,
	) ([]model.RemoteCacheEntry, string, error)
	// Exact lists only this canonical path, never its descendants.
	Exact(
		ctx context.Context,
		repoKey, path string,
		p kernel.Page,
	) ([]model.RemoteCacheEntry, string, error)
	Check(ctx context.Context, token model.RemoteCacheToken) error
	// Put replaces state after checking the token, advances Revision, and owns
	// the raw metadata blob reference when ItemID is zero. Item-backed entries
	// do not add another reference. Empty entries are durable path tombstones.
	Put(
		ctx context.Context,
		token model.RemoteCacheToken,
		entry model.RemoteCacheEntry,
	) (model.RemoteCacheEntry, error)
	Negative(ctx context.Context, token model.RemoteCacheToken, until time.Time) error
	Revalidate(ctx context.Context, token model.RemoteCacheToken, at time.Time) error
	Touch(ctx context.Context, token model.RemoteCacheToken, at time.Time) error
	// Evict clears exactly this representation and advances its epoch. It
	// releases raw references; callers remove item-backed content separately.
	Evict(ctx context.Context, token model.RemoteCacheToken) error
	// Invalidate fences every matching path, including empty in-flight entries.
	// MetadataOnly expires metadata and clears negatives, preserving binaries.
	// Otherwise entries become tombstones and release raw metadata references.
	Invalidate(ctx context.Context, repoKey, prefix string, metadataOnly bool) (int64, error)
	State(ctx context.Context, repoKey string) (model.RemoteSourceState, error)
	PutState(ctx context.Context, repoKey string, state model.RemoteSourceState) error
}

// NewRemoteSource creates a durable identity, never a configuration hash.
func NewRemoteSource(key string) model.SourceIdentity {
	return model.SourceIdentity{
		Kind: model.ExternalHTTP,
		ID:   key,
		Repository: model.RepositoryIncarnation(
			rand.Text(),
		),
		Generation: model.SourceGeneration(rand.Text()),
	}
}

// RemoteConfigChanged reports source identity changes, excluding policy-only
// settings. Policy changes still fence in-flight writes via PolicyRevision.
func RemoteConfigChanged(a, b repo.Config) bool {
	return !reflect.DeepEqual(RemoteSourceConfig(a), RemoteSourceConfig(b))
}

// RemoteSourceConfig removes settings that govern use of cached bytes rather
// than their upstream identity. Credential resolvers can use this projection.
func RemoteSourceConfig(c repo.Config) repo.Config {
	c.Description, c.Notes = "", ""
	c.RetrievalCachePeriodSecs, c.MissedRetrievalCachePeriodSecs, c.MetadataRetrievalTimeoutSecs = nil, nil, nil
	c.AssumedOfflinePeriodSecs, c.UnusedArtifactsCleanupPeriodHours = nil, nil
	c.HardFail, c.Offline, c.StoreArtifactsLocally = nil, nil, nil
	c.IncludesPattern, c.ExcludesPattern = nil, nil
	c.BlackedOut = nil
	return c
}

// RemotePolicyChanged reports a noncosmetic configuration change. Tokens must
// be re-authorized after these edits even when content retains its identity.
func RemotePolicyChanged(a, b repo.Config) bool {
	a.Description, a.Notes, b.Description, b.Notes = "", "", "", ""
	return !reflect.DeepEqual(a, b)
}

// RemoteCacheID bounds a SQL key independently of path length. The actual key
// is stored and compared, so a hash collision cannot reuse a representation.
func RemoteCacheID(k model.RemoteCacheKey) string {
	b, _ := json.Marshal(k)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ValidateRemoteKey requires canonical paths and non-secret bounded variants.
func ValidateRemoteKey(k model.RemoteCacheKey) error {
	_, _, _, err := kernel.SplitItemPath(k.Path)
	if k.RepoKey == "" || err != nil || strings.Trim(k.Path, "/") != k.Path ||
		len(k.Variant) > 512 ||
		len(k.SettingsRevision) > 512 {
		return fmt.Errorf("remote cache key: %w", kernel.ErrInvalid)
	}
	return nil
}

// RemotePathMatches includes a prefix itself and its descendants, not siblings.
func RemotePathMatches(path, prefix string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

// ValidateRemoteEntry refuses impossible body references and unknown kinds.
//
//nolint:gocyclo // Each branch refuses one field of an entry before it is stored.
func ValidateRemoteEntry(e model.RemoteCacheEntry) error {
	if s := e.Smart; s != nil {
		if e.ItemID == 0 || s.Downloads < 0 || s.Claimed < 0 || s.Claimed > s.Downloads ||
			s.Uncertain < 0 || s.Uncertain > s.Claimed || len(s.OwnedProperties) > 1024 || len(s.LocalProperties) > 4096 {
			return fmt.Errorf("remote Smart state: %w", kernel.ErrInvalid)
		}
		for key, values := range s.OwnedProperties {
			if model.ValidatePropertyKey(key) != nil || len(values) == 0 || len(values) > 64 {
				return fmt.Errorf("remote Smart property: %w", kernel.ErrInvalid)
			}
		}
		for key := range s.LocalProperties {
			if model.ValidatePropertyKey(key) != nil {
				return fmt.Errorf("remote Smart property ownership: %w", kernel.ErrInvalid)
			}
		}
	}
	switch e.Kind {
	case "", model.RemoteUnique, model.RemoteMutable, model.RemoteMetadata, model.RemoteDerived:
	default:
		return fmt.Errorf("remote cache kind: %w", kernel.ErrInvalid)
	}
	if e.MetadataBaseURL != "" {
		u, err := url.Parse(e.MetadataBaseURL)
		if err != nil || e.Kind != model.RemoteMetadata || u.Hostname() == "" ||
			(u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("remote metadata response base: %w", kernel.ErrInvalid)
		}
	}
	if e.ItemID < 0 || e.Blob.Size < 0 || (e.ItemID > 0 && e.Blob.Checksums.SHA256 == "") {
		return fmt.Errorf("remote cache body: %w", kernel.ErrInvalid)
	}
	return nil
}
