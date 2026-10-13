package types

import "time"

// ReplicaState is where one member of a blob mirror stands with one blob
// (decision 0028).
type ReplicaState string

// Replica states.
const (
	// ReplicaPresent: the member holds the blob.
	ReplicaPresent ReplicaState = "present"
	// ReplicaPending: the member should hold the blob and the repair worker
	// will copy it there.
	ReplicaPending ReplicaState = "pending"
	// ReplicaDeleting: the blob was deleted and this member's copy still has
	// to be removed.
	ReplicaDeleting ReplicaState = "deleting"
	// ReplicaFailed: repair gave up after repeated attempts; an operator or a
	// manual repair pass has to act.
	ReplicaFailed ReplicaState = "failed"
)

// BlobReplica records one mirror member's copy of one blob. The mirror keeps
// this index so reads go straight to a holder and repair works from a queue,
// where Artifactory's sharding has no index and probes every shard.
type BlobReplica struct {
	SHA256    string
	Member    string
	State     ReplicaState
	Size      int64
	Attempts  int
	LastError string
	Updated   time.Time
}
