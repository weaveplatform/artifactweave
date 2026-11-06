// Bridge (P9d): declared in migration/platform/blob; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/blob"

type (
	Blob      = blob.Blob
	BlobState = blob.BlobState
)

const (
	BlobLive     = blob.BlobLive
	BlobDeleting = blob.BlobDeleting
	BlobDeleted  = blob.BlobDeleted
)
