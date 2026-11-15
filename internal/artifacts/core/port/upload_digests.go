package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// UploadDigestStore keeps the SHA-1 and MD5 an upload computed, by the
// content's SHA-256, for a protocol whose item is stored by a later
// request: a Hugging Face LFS object, uploaded before the commit that names
// its path. Every blob store answers Stat with the size and SHA-256 alone,
// so without it the commit would store the item without the two checksums
// 7.x keeps for every file (#1581).
type UploadDigestStore interface {
	// Put records d's SHA-1 and MD5 under d.SHA256; kernel.ErrInvalid when
	// any of the three is empty. The SHA-256 names the content, so a
	// second Put of the same content keeps the first row.
	Put(ctx context.Context, d blob.Digests) error
	// Get returns the SHA-256, SHA-1 and MD5 recorded for sha256;
	// kernel.ErrNotFound when none is.
	Get(ctx context.Context, sha256 string) (blob.Digests, error)
}
