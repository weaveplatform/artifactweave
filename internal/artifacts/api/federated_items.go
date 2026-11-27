package api

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ItemOperation distinguishes the source change for federation monitoring.
// Delivery still carries and applies the item's whole state.
type ItemOperation string

// Item operations used by the federation event counters.
const (
	ItemCreate     ItemOperation = "create"
	ItemUpdate     ItemOperation = "update"
	ItemProperties ItemOperation = "properties"
	ItemDelete     ItemOperation = "delete"
)

// ItemChange is the state of one file or folder of a repository after a
// change, as the item service committed it: what a member of a federated
// repository sends the others (decision 0022, What propagates). A deleted
// item keeps its identity and loses its state. Properties is the item's
// whole set after the change, never a difference, so whichever change wins
// the conflict rule leaves the same item on every member.
type ItemChange struct {
	Operation  ItemOperation
	Repository string
	// Path is repository-relative, without a leading slash.
	Path    string
	Folder  bool
	Deleted bool
	Size    int64
	// Actual is the content's checksums; Original what the client declared.
	Actual     kernel.Checksums
	Original   kernel.Checksums
	Created    time.Time
	CreatedBy  string
	Modified   time.Time
	ModifiedBy string
	Properties map[string][]string
}

// ItemFeed hears of each change a client or a policy makes to a file or
// folder of a local or federated repository: a deploy, a copy or move
// landing, a restore, a checksum recorded, a property change and each item
// a delete or a move removes, a removed folder after the files below it.
// The item service calls it inside the change's transaction, so what it
// records commits or rolls back with the change, and a failure rolls the
// change back. It is not told of what a format derives (Maven metadata, an
// npm dist-tag moved by a deploy's hook), of remote caches, or of what
// FederatedItems applies: a member recomputes the first itself, and
// reporting the last would send a change back to where it came from.
type ItemFeed interface {
	ItemChanged(ctx context.Context, c ItemChange) error
}

// FederatedItemReader lists current item states for snapshots and status reads.
type FederatedItemReader interface {
	List(ctx context.Context, repoKey, after string, limit int, properties bool) ([]ItemChange, error)
}

// FederatedItems applies another member's changes to a federated
// repository (decision 0022): each call joins the transaction ctx carries,
// runs the format's hooks as the change it repeats does, so this instance
// recomputes its own format metadata and package index, and is not
// reported to the ItemFeed.
type FederatedItems interface {
	// Put makes the file or folder at c.Path what c says, creating the
	// folders above it. A file's content must be in the blob store under
	// c.Actual.SHA256; a file of the same content keeps its record and
	// takes c's properties and original checksums. kernel.ErrConflict when
	// a folder is in the way of a file or a file in the way of a folder;
	// kernel.ErrNotFound when the repository does not exist.
	Put(ctx context.Context, c ItemChange) error
	// Delete removes the file at path, into the trash can when it is on,
	// or the folder at path when nothing is left in it. Nothing at path is
	// not an error.
	Delete(ctx context.Context, repoKey, path string, folder bool) error
	// Discard removes whatever is at path, a folder with everything below
	// it, into the trash can when it is on: a forced full sync's removal of
	// what the member it resynchronises from does not hold. Nothing at path
	// is not an error.
	Discard(ctx context.Context, repoKey, path string) error
	// List returns up to limit of the repository's files and folders whose
	// paths follow after in byte order, in that order, as their state
	// stands: what a full sync's snapshot lists. properties false leaves
	// Properties out, for a caller that compares only paths.
	List(ctx context.Context, repoKey, after string, limit int, properties bool) ([]ItemChange, error)
	// Holds reports whether a file of the repository has the content
	// sha256, so a member resynchronising from this instance may fetch it,
	// whether or not a change of the stream wrote the file.
	Holds(ctx context.Context, repoKey, sha256 string) (bool, error)
}
