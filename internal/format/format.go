// Package format is the contract between the item service and the package
// formats (decision 0032): the hooks every format implements, the optional
// interfaces a format adds for what it needs, the environment they run in,
// and the Rejection a format refuses a request with. The item service calls
// the hooks on every write, whichever API carried it, so a format's package
// index (decision 0018) and derived items stay consistent with the item
// tree, which remains the source of truth.
//
// Each ecosystem has a subpackage named after it (repo-layout.md), and
// internal/format/formats picks one for a package type. A format package
// never imports internal/server, so formats speak to clients only through
// Rejection.
package format

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handler is one package type's required hooks. All run inside the
// transaction that changes the item, so an error rolls the change back.
type Handler interface {
	// Deployed runs after a file was stored at it (new or overwritten), by a
	// deploy, a copy, a move or a restore, once its properties are set.
	Deployed(ctx context.Context, env Env, c repo.Config, it types.Item) error
	// Deleting runs for each file before it is removed from the tree.
	Deleting(ctx context.Context, env Env, c repo.Config, it types.Item) error
	// Deleted runs once after a delete or a move removed items from the tree
	// and the item service pruned the versions left without files. A format
	// whose versions or packages depend on more than their own files (a
	// Docker tag exists while its folder holds a manifest) corrects the
	// index here.
	Deleted(ctx context.Context, env Env, c repo.Config, removed []types.Item) error
}

// Env is what a hook runs with. The item service builds one per operation;
// inside a hook, ctx carries the operation's transaction, which Store,
// Derive and Enqueue join.
type Env struct {
	// Store is the metadata store.
	Store store.Store
	// Blobs reads stored content, such as the file a hook indexes. Formats
	// write content only through Derive and remove it only through
	// Underive.
	Blobs Blobs
	// Now is the clock.
	Now func() time.Time
	// Layouts resolves repository layouts by name, compiling each at most
	// once per operation.
	Layouts Layouts
	// Derive writes a derived item, such as maven-metadata.xml, inside the
	// current transaction without calling any hook, so a format's own
	// output never re-enters it. A format must not derive an item another
	// format would need to index.
	Derive func(ctx context.Context, d Derived) (types.Item, error)
	// Underive removes the file at path, which the format derived, without
	// any hook and past the trash can, releasing its content; nothing at
	// path is no error. A restore would bring back data the format
	// recomputes anyway.
	Underive func(ctx context.Context, c repo.Config, cached bool, path string) error
	// Remove deletes the files at paths, of c's own content, as a client's
	// delete does: through every hook, the format's own included, and into
	// the trash can when it is on, recorded as deleted by by. It then
	// prunes the folders those deletes left empty, up to the repository
	// root. A path with nothing at it is skipped. It is one transaction, so
	// a failure removes none. Derived work removes deployed files with it,
	// such as Maven's maxUniqueSnapshots cleanup.
	Remove func(ctx context.Context, c repo.Config, paths []string, by string) error
	// Enqueue asks the persistent work queue for kind's work on key
	// (decision 0033). Called with a hook's ctx, the work is asked for
	// exactly when the write commits, and not at all after a rollback.
	Enqueue func(ctx context.Context, kind, key string, payload []byte) error
	// Move moves the file at from to to, both in c, as a move by the REST
	// API makes it, with every hook, by as its mover, and never over an
	// item at to. It returns why the file did not move, or "" once it has.
	// It is for a format's Work, such as npm's strict-layout conversion,
	// which moves files a client stored where the format cannot index them.
	Move func(ctx context.Context, c repo.Config, from, to, by string) (string, error)
	// Inspection is what the format's Inspect returned for the file being
	// written. It is nil when the format inspects nothing, and on a
	// restore, which runs inside its caller's transaction: a hook that needs
	// one then inspects the content itself.
	Inspection any
}

// Blobs reads stored content: the read half of blob.Store.
type Blobs interface {
	// Open returns the content stored under a SHA-256 for random access;
	// the caller closes it.
	Open(ctx context.Context, sha256 string) (io.ReadSeekCloser, blob.Info, error)
	// Stat returns a blob's Info without opening it.
	Stat(ctx context.Context, sha256 string) (blob.Info, error)
}

// Layouts resolves a repository layout by name: one the descriptor stored,
// else a bundled one.
type Layouts interface {
	Layout(ctx context.Context, name string) (*layout.Layout, error)
}

// Derived is an item a format writes itself. Its content goes into the blob
// store and the item into the tree, replacing a file at Path; a folder in
// the way is an error.
type Derived struct {
	Repo   repo.Config
	Cached bool
	// Path is repository-relative, without a leading slash.
	Path       string
	Body       io.Reader
	Properties types.Properties
	// By is recorded as the item's creator and modifier.
	By string
}

// Op is the kind of write Admit is asked about.
type Op int

// Writes a format admits.
const (
	// OpDeploy stores content at a path: by upload, by checksum, or as an
	// entry of an exploded archive.
	OpDeploy Op = iota + 1
	// OpChecksum records a client checksum for the file at the path, from a
	// sidecar upload ("a/b.jar.sha1" names the file "a/b.jar").
	OpChecksum
	// OpCopy and OpMove land an existing file at the path.
	OpCopy
	OpMove
)

// Write is a file write as a format sees it before it happens.
type Write struct {
	Op Op
	// Path is the repository-relative target. Admit may change it, and the
	// write then lands there.
	Path   string
	Cached bool
	// Properties are set on the item with the write: a deploy's matrix
	// parameters, or the properties a copy or move carries over. Admit may
	// add or change them.
	Properties types.Properties
	// Size and Checksums describe the content. For OpChecksum, Checksums is
	// empty and Size is the length of the sidecar's body as the client
	// declared it, or -1.
	Size      int64
	Checksums types.Checksums
	// From is the source of a copy or move.
	From types.Item
}

// Inspector is a format that reads deployed content before the write's
// transaction, so no decompression or parsing holds the database's write
// lock. The item service calls Inspect once the content is in the blob
// store and hands the result to Admit and Deployed as Env.Inspection. A
// write the transaction then refuses leaves the content for garbage
// collection.
type Inspector interface {
	Inspect(ctx context.Context, env Env, c repo.Config, w Write) (any, error)
}

// Admitter is a format that checks a file write before it is made: a
// deploy or a checksum upload inside the write's transaction, and the
// target of a copy or move while it is planned. It returns the write as it
// should happen, with the path rewritten or properties added, or refuses
// it with a *Rejection. A refused copy or move target is reported and
// skipped, as other per-item failures are. The generic format, which every
// format embeds, checks blackedOut, includesPattern and excludesPattern
// here; a format that overrides Admit calls those checks itself.
type Admitter interface {
	Admit(ctx context.Context, env Env, c repo.Config, w Write) (Write, error)
}

// WriteGuard rechecks invariants that depend on concurrent writes inside the
// transaction, before a deploy, copy or move lands. Unlike Admit, which also
// plans transfers outside a transaction, this hook may acquire database locks
// that must survive until commit. A format Rejection skips a transfer item,
// as planning refusals do; other errors roll back the operation.
// Restores bypass admission and this guard, preserving their existing semantics.
type WriteGuard interface {
	GuardWrite(ctx context.Context, env Env, c repo.Config, w Write) error
}

// FolderAdmitter is a format that checks a folder create before it is
// made, as Admitter checks a file write: every folder the item service's
// MakeFolder makes, which is a client's folder create and the folder
// entries of an archive exploded without atomicity. The folders a file
// write creates above its file are not asked, since Admit was asked about
// the file.
type FolderAdmitter interface {
	AdmitFolder(ctx context.Context, env Env, c repo.Config, path string) error
}

// AnnotationAdmitter is a format that checks a change to an item's
// properties before it is made, such as the Terraform backend's, whose
// lock properties only its own API changes (decision 0041). The item
// service asks it of every change Service.Annotate makes: the properties
// API, AQL properties.update, a folder create's matrix parameters and a
// promotion's. A deploy's properties reach Admit with the write, and a
// restore brings back, or a copy or move carries, properties that were
// set already, so none of those asks.
type AnnotationAdmitter interface {
	AdmitAnnotation(ctx context.Context, env Env, c repo.Config, it types.Item, a Annotation) error
}

// Download is a file a client asks for.
type Download struct {
	// Path is the requested repository-relative path.
	Path   string
	Cached bool
	// Matrix holds the request's matrix parameters, which the file served
	// must match.
	Matrix types.Matrix
	// Readable reports whether the caller may read a file of the
	// repository; nil reads every file.
	Readable func(fullPath string) bool
}

// Resolver is a format whose downloads may name a file other than the one
// requested, such as a Maven version token. Resolve runs before a GET or
// HEAD looks the path up and returns the path to look up instead.
type Resolver interface {
	Resolve(ctx context.Context, env Env, c repo.Config, d Download) (string, error)
}

// Annotation is a change to an item's properties.
type Annotation struct {
	// Set holds the keys set or replaced, with their new values.
	Set types.Properties
	// Removed lists the keys removed.
	Removed []string
	// At is when the change was made. A hook that writes properties in
	// turn writes them at At, so they read as part of the same change
	// (types.Item.PropertiesModified) rather than a later one.
	At time.Time
}

// Annotator is a format whose index follows item properties, as npm's
// dist-tags do. The item service calls Annotated inside the transaction of
// every property write: the properties API, matrix parameters on a deploy
// or a folder, AQL properties.update, a copy or move carrying properties
// over, a restore, and a promotion tagging what it promoted.
type Annotator interface {
	Annotated(ctx context.Context, env Env, c repo.Config, it types.Item, a Annotation) error
}

// Reindexer is a format that can rebuild its index of a repository from
// the item tree, for a JFrog reindex operation and for maintenance. It
// manages its own transactions, so a large repository need not be rebuilt
// in one.
type Reindexer interface {
	Reindex(ctx context.Context, env Env, c repo.Config) error
}

// PathReindexer is a Reindexer that can rebuild the part of its index that
// the files under one path give, for Helm's partial reindex. The item
// service rebuilds the whole repository for a Reindexer that is not one,
// which leaves the same index at a greater cost.
type PathReindexer interface {
	Reindexer
	ReindexPath(ctx context.Context, env Env, c repo.Config, path string) error
}

// The kinds of work the persistent work queue runs for the JFrog reindex
// operations of every format (decision 0033), which 7.x schedules rather
// than runs before it answers. The item service runs them through the
// format of the repository's package type, so a Reindexer needs no kind of
// its own, and a repository that is gone, or whose format keeps no index,
// owes nothing.
const (
	// KindReindex rebuilds a repository's index, keyed by the repository's
	// key: requests made before a run begins coalesce into it.
	KindReindex = "reindex"
	// KindReindexPath rebuilds the part of a repository's index that the
	// files under a path give, keyed by "<repository key>/<path>", since
	// repository keys hold no slash. A key of its own per path keeps one
	// path's request from taking the place of another's, as the payload of
	// a coalesced request would.
	KindReindexPath = "reindex.path"
)

// WorkRequest is the payload of the work a JFrog operation queues: who
// asked for it.
type WorkRequest struct {
	// By is recorded as the mover of what npm's strict-layout conversion
	// moves.
	By string `json:"by,omitempty"`
}

// WorkRef names one item of the persistent work queue: a kind and a key.
type WorkRef struct {
	Kind, Key string
}

// Worker is a format that queues derived work with Env.Enqueue and runs it
// from the persistent work queue (decision 0033), such as Maven's metadata.
type Worker interface {
	// WorkKinds are the kinds the format enqueues: code constants, unique
	// across formats.
	WorkKinds() []string
	// Work runs one item of one of the format's kinds, outside any
	// transaction. It must be idempotent and read the state it derives from
	// afresh, since an item can run again and coalesces every request made
	// before the run began.
	Work(ctx context.Context, env Env, item types.WorkItem) error
	// Owed names the work a download of path from c must see done before
	// it is read, so a client never reads derived data that lags a
	// completed write.
	Owed(c repo.Config, cached bool, path string) []WorkRef
}

// Class says what kind of content an item is, for cache expiry (P9),
// replication (P11) and federation (P13).
type Class int

// Classes of item.
const (
	// ClassUnique is content only its deployer can supply again, such as a
	// jar or an image layer.
	ClassUnique Class = iota + 1
	// ClassMetadata is deployed content that describes others and may
	// change under the same path, such as a Docker tag's manifest.
	ClassMetadata
	// ClassDerived is written by the format itself (Env.Derive), so
	// replication and federation skip it and each side recomputes it.
	ClassDerived
)

// Classifier is a format that tells its classes of item apart.
type Classifier interface {
	Classify(c repo.Config, path string) Class
}

// ClassOf classifies the item at path in c: h's own class when it is a
// Classifier, unique otherwise.
func ClassOf(h Handler, c repo.Config, path string) Class {
	if cl, ok := h.(Classifier); ok {
		return cl.Classify(c, path)
	}
	return ClassUnique
}

// SidecarOwner is a format whose repositories take every upload named as a
// checksum sidecar ("a/b.jar.sha1") as a checksum of its file, as
// Artifactory does: Admit sees each as an OpChecksum, whether or not the
// file exists, and one whose file is missing is refused. In the other
// formats such an upload without its file is stored as a file of its own.
type SidecarOwner interface {
	OwnsSidecars()
}

// Accepted is a format's answer that a write succeeds without being made.
// Artifactory takes some uploads of what it computes itself, such as
// Maven's snapshot metadata and the checksums of any metadata, with a
// success status and stores nothing. Admit returns it, as an error, only
// for an OpDeploy or an OpChecksum; the HTTP layer answers Status with an
// empty body, and an exploded archive skips the entry.
type Accepted struct {
	Status int
}

func (a *Accepted) Error() string {
	return fmt.Sprintf("format: accepted with status %d and not stored", a.Status)
}

// Rejection is a format's refusal of a request. The HTTP layer answers it
// with Status and Message in the envelope of the surface the request
// arrived on.
type Rejection struct {
	Status  int
	Message string
}

// Reject returns a Rejection with a formatted message.
func Reject(status int, format string, args ...any) *Rejection {
	return &Rejection{Status: status, Message: fmt.Sprintf(format, args...)}
}

func (r *Rejection) Error() string { return r.Message }
