// Bridge (P9d): declared in migration/platform/blob/mirror; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"

type (
	ReplicaState = mirror.ReplicaState
	BlobReplica  = mirror.BlobReplica
)

const (
	ReplicaPresent  = mirror.ReplicaPresent
	ReplicaPending  = mirror.ReplicaPending
	ReplicaDeleting = mirror.ReplicaDeleting
	ReplicaFailed   = mirror.ReplicaFailed
)
