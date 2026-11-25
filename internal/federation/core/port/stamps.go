package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// ItemStampStore keeps the conflict rule's record of each item of a
// federated repository (decision 0021, Conflict rule): its stamp, its
// tombstone, and a change waiting for its blob. Every call joins the
// transaction ctx carries, so a stamp commits with the change it records.
type ItemStampStore interface {
	// Stamp returns the record of the item at path; kernel.ErrNotFound when
	// there is none.
	Stamp(ctx context.Context, repoKey, path string) (model.ItemStamp, error)
	// Record stores s, replacing the item's record: a change this instance
	// made, which its clock has already ordered after the record it read.
	Record(ctx context.Context, s model.ItemStamp) error
	// Replace stores next in place of the record stamped prev, or where
	// there was none when prev is nil, and reports whether it did: false
	// when another transaction has changed the record since prev was
	// read. On PostgreSQL it waits for that transaction, so of a change
	// applied and a local write racing on one item, the second to commit
	// finds the first's record and is retried rather than overwriting it.
	Replace(ctx context.Context, prev *model.Stamp, next model.ItemStamp) (bool, error)
	// PendingOn returns the records of repoKey waiting for the blob sha256,
	// ordered by path.
	PendingOn(ctx context.Context, repoKey, sha256 string) ([]model.ItemStamp, error)
	// Holds reports whether a live record of repoKey has the content sha256.
	Holds(ctx context.Context, repoKey, sha256 string) (bool, error)
	// PruneTombstones removes the tombstones stamped before before, of
	// every repository, and returns how many.
	PruneTombstones(ctx context.Context, before outbox.HLC) (int64, error)
	// List returns up to limit of repoKey's records, tombstones and
	// changes pending a blob included, whose paths follow after in byte
	// order, in that order: what a full sync merges with the items.
	List(ctx context.Context, repoKey, after string, limit int) ([]model.ItemStamp, error)
	// Remove removes the record of the item at path, tombstone included,
	// and with below the records of every item under it as a folder: what
	// a forced full sync discards. None there is not an error.
	Remove(ctx context.Context, repoKey, path string, below bool) error
	// Clear removes every record of repoKey, when the repository is
	// deleted or stops being federated.
	Clear(ctx context.Context, repoKey string) error
}
