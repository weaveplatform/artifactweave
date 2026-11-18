package port

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Store is every store the artifacts module owns. All of them take part in
// the caller's unit of work (tx.Runner); ColdBlobs and ColdReplicas index
// the cold tier with the same code as the hot ones.
type Store interface {
	tx.Runner
	Repos() RepoStore
	RemoteCache() RemoteCacheStore
	VirtualCopies() virtual.CopyStore
	Items() ItemStore
	Blobs() BlobIndex
	Replicas() ReplicaIndex
	Trash() TrashStore
	Packages() PackageStore
	GitLFSLocks() GitLFSLockStore
	BowerRegistrations() BowerRegistrationStore
	NPMExternals() NPMExternalStore
	UploadDigests() UploadDigestStore
	Builds() BuildStore
	ArchivedItems() ArchivedItemStore
	ColdBlobs() BlobIndex
	ColdReplicas() ReplicaIndex
}
