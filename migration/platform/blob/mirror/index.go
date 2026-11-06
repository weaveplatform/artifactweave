package mirror

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BlobIndex is the blob index the repair worker reads reference counts from.
type BlobIndex interface {
	// Get returns the row for sha256, in whatever state.
	Get(ctx context.Context, sha256 string) (types.Blob, error)
}

// ReplicaIndex records which members hold which blobs. The mirror never
// calls Get.
type ReplicaIndex interface {
	// Put creates or replaces the record (r.SHA256, r.Member); ErrInvalid
	// without both.
	Put(ctx context.Context, r types.BlobReplica) error
	// ForBlob returns a blob's records ordered by member.
	ForBlob(ctx context.Context, sha256 string) ([]types.BlobReplica, error)
	// List returns matching records oldest update first, by (Updated, SHA256,
	// Member), which is the order the repair worker drains them in.
	List(
		ctx context.Context,
		f store.ReplicaFilter,
		p store.Page,
	) ([]types.BlobReplica, string, error)
	// Delete removes one record.
	Delete(ctx context.Context, sha256, member string) error
	// DeleteBlob removes every record of a blob and returns how many.
	DeleteBlob(ctx context.Context, sha256 string) (int64, error)
	// Counts returns the number and size of records per member and state,
	// ordered by (member, state).
	Counts(ctx context.Context) ([]store.ReplicaCount, error)
	// UnderReplicated counts the blobs with fewer than redundancy present
	// records. A blob with a deleting record is being deleted and is not
	// counted; neither is a blob with no records at all, which the repair
	// worker's backfill brings into the index.
	UnderReplicated(ctx context.Context, redundancy int) (int64, error)
	// SoleCopies counts, per member, the blobs whose only present record is
	// on that member. Blobs being deleted are left out, as UnderReplicated
	// leaves them out. A member holding sole copies cannot be removed from
	// a mirror without losing them: repair never reads from a member that
	// is no longer configured.
	SoleCopies(ctx context.Context) (map[string]int64, error)
}
