package wire

// StorageSummaryMirrors is artifactweave's addition to the storage summary
// (GET /artifactory/api/storageinfo, decision 0028). Artifactory reports
// nothing per shard, so these fields have no JFrog counterpart; they follow
// the summary's camelCase so the document reads as one.
type StorageSummaryMirrors struct {
	BlobMirrors []BlobMirror `json:"blobMirrors"`
}

// BlobMirror is one mirrored blob tier, "blob" or "cold_blob".
type BlobMirror struct {
	Tier        string `json:"tier"`
	Zone        string `json:"zone,omitempty"`
	Redundancy  int    `json:"redundancy"`
	WriteQuorum int    `json:"writeQuorum"`
	// UnderReplicatedBlobs counts blobs with fewer present copies than
	// Redundancy, not counting blobs being deleted.
	UnderReplicatedBlobs int64              `json:"underReplicatedBlobs"`
	Members              []BlobMirrorMember `json:"members"`
}

// BlobMirrorMember is one member store of a BlobMirror.
type BlobMirrorMember struct {
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	Backend      string `json:"backend,omitempty"`
	WriteEnabled bool   `json:"writeEnabled"`
	// Available is false while the member's breaker is open.
	Available        bool  `json:"available"`
	AverageLatencyMs int64 `json:"averageLatencyMs"`
	// Replicas is keyed by replica state (present, pending, deleting,
	// failed) and always holds all four.
	Replicas map[string]ReplicaTally `json:"replicas"`
}

// ReplicaTally counts a member's replica records in one state; Size is
// Bytes human-formatted as the rest of the summary is.
type ReplicaTally struct {
	Count int64  `json:"count"`
	Bytes int64  `json:"bytes"`
	Size  string `json:"size"`
}
