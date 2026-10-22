package types

import "time"

// RemoteKind distinguishes freshness from the metadata-only zap policy.
type RemoteKind string

// Remote cache representation kinds. Mutable payloads expire but are not zapped.
const (
	RemoteUnique   RemoteKind = "unique"
	RemoteMutable  RemoteKind = "mutable"
	RemoteMetadata RemoteKind = "metadata"
	RemoteDerived  RemoteKind = "derived"
)

// RemoteCacheKey identifies one raw upstream representation. Variant and
// SettingsRevision are opaque non-secret identifiers, never raw signed queries.
type RemoteCacheKey struct {
	RepoKey          string `json:"repo_key"`
	Path             string `json:"path"`
	Variant          string `json:"variant"`
	SettingsRevision string `json:"settings_revision"`
}

// RemoteCacheToken fences a fetch started outside the transaction. Epoch
// survives deletion of a cached path; Revision serializes competing completions.
type RemoteCacheToken struct {
	PolicyRevision int64          `json:"policy_revision"`
	Key            RemoteCacheKey `json:"key"`
	Source         SourceIdentity `json:"source"`
	Epoch          int64          `json:"epoch"`
	Revision       int64          `json:"revision"`
}

// RemoteCacheEntry records retrieval state independently of Item.Updated.
// ItemID names an ingested artifact. Raw metadata instead owns Blob directly,
// outside the item tree, so a format reindex cannot overwrite the upstream list.
// An entry can keep its positive content while NegativeUntil records a later 404.
type RemoteCacheEntry struct {
	// Smart belongs to this exact item/source/credential revision. Raw upstream
	// properties are independent of locally edited annotations and byte TTLs.
	Smart *RemoteSmartState `json:"smart,omitempty"`
	// MetadataBaseURL is private retrieval state for resolving relative links.
	// It must not include userinfo, query or fragment, or become a response header.
	MetadataBaseURL string `json:"metadata_base_url,omitempty"`
	// OriginalChecksums retains unverified upstream claims for pass-thru delivery.
	// Blob.Checksums always identifies the bytes actually ingested.
	OriginalChecksums Checksums        `json:"original_checksums,omitempty"`
	Token             RemoteCacheToken `json:"token"`
	Kind              RemoteKind       `json:"kind"`
	ItemID            int64            `json:"item_id"`
	Blob              Blob             `json:"blob"`
	ETag              string           `json:"etag"`
	LastModified      string           `json:"last_modified"`
	ContentType       string           `json:"content_type"`
	CacheControl      string           `json:"cache_control"`
	Vary              string           `json:"vary"`
	FetchedAt         time.Time        `json:"fetched_at"`
	ValidatedAt       time.Time        `json:"validated_at"`
	LastUsed          time.Time        `json:"last_used"`
	NegativeUntil     time.Time        `json:"negative_until"`
}

// RemoteSmartState is additive private cache state, persisted in the existing
// cache document. A statistics claim is committed before its HTTP submission:
// counts in Uncertain are never retried without a peer idempotency contract.
type RemoteSmartState struct {
	OwnedProperties Properties      `json:"owned_properties,omitempty"`
	LocalProperties map[string]bool `json:"local_properties,omitempty"`
	PropertiesAt    time.Time       `json:"properties_at"`
	OriginAt        time.Time       `json:"origin_at"`
	OriginAbsent    bool            `json:"origin_absent"`
	Downloads       int64           `json:"downloads"`
	Claimed         int64           `json:"claimed"`
	Uncertain       int64           `json:"uncertain"`
	LastDownload    time.Time       `json:"last_download"`
	LastDownloadBy  string          `json:"last_download_by,omitempty"`
}

// RemoteSourceState is shared by paths of a source. Success/failure scheduling
// belongs to the remote policy; the store provides generation-fenced durability.
type RemoteSourceState struct {
	Smart              *RemoteSmartPeer `json:"smart,omitempty"`
	SettingsRevision   string           `json:"settings_revision"`
	PolicyRevision     int64            `json:"policy_revision"`
	Source             SourceIdentity   `json:"source"`
	Revision           int64            `json:"revision"`
	ConnectionFailures int              `json:"connection_failures"`
	OfflineUntil       time.Time        `json:"offline_until"`
	RetryAfterUntil    time.Time        `json:"retry_after_until"`
}

// RemoteSmartPeer retains only sanitized discovery evidence, never the remote
// repository configuration (which may contain credentials or private URLs).
type RemoteSmartPeer struct {
	BaseURL    string    `json:"base_url"`
	Repository string    `json:"repository"`
	Class      string    `json:"class"`
	Version    string    `json:"version"`
	CheckedAt  time.Time `json:"checked_at"`
}
