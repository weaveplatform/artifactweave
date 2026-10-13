package store

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BundleFilter narrows release bundle listings.
type BundleFilter struct {
	Name       string
	Origin     types.BundleOrigin
	ProjectKey string
}

// BundleStore persists release bundles v1 (decision 0023) with their frozen
// artifact lists, retention keep marks and audit, and exports.
type BundleStore interface {
	// Create stores b with its artifacts, whose BundleName and BundleVersion
	// are set from b; ErrConflict when (name, version) exists or two
	// artifacts share a target path.
	Create(ctx context.Context, b types.ReleaseBundle, artifacts []types.BundleArtifact) error
	// Get returns one bundle version.
	Get(ctx context.Context, name, version string) (types.ReleaseBundle, error)
	// Update replaces the bundle's record; its artifacts are unchanged.
	Update(ctx context.Context, b types.ReleaseBundle) error
	// ReplaceArtifacts replaces the artifact list of an open bundle;
	// ErrConflict once the bundle is signed (types.BundleState.Editable).
	ReplaceArtifacts(ctx context.Context, name, version string, artifacts []types.BundleArtifact) error
	// Delete removes the bundle version with its artifacts, keep mark and
	// export; the audit trail is kept.
	Delete(ctx context.Context, name, version string) error
	// List returns matching bundles ordered by (name, version).
	List(ctx context.Context, f BundleFilter, p Page) ([]types.ReleaseBundle, string, error)
	// Artifacts returns a bundle's artifacts ordered by target path;
	// ErrNotFound when the bundle does not exist.
	Artifacts(ctx context.Context, name, version string) ([]types.BundleArtifact, error)

	// SetKeep marks or unmarks a bundle version as kept from retention;
	// marking twice or unmarking an unmarked version is not an error.
	SetKeep(ctx context.Context, name, version string, keep bool) error
	// Kept reports whether a bundle version is marked kept; ErrNotFound when
	// the bundle does not exist.
	Kept(ctx context.Context, name, version string) (bool, error)
	// AddAudit records a retention operation. The bundle need not exist:
	// the audit outlives the versions retention deletes.
	AddAudit(ctx context.Context, a types.BundleRetentionAudit) error
	// Audits returns a bundle's retention operations ("" for every bundle),
	// newest first by Created.
	Audits(ctx context.Context, name string) ([]types.BundleRetentionAudit, error)

	// PutExport creates or replaces a bundle version's export record;
	// ErrNotFound when the bundle does not exist.
	PutExport(ctx context.Context, e types.BundleExport) error
	// Export returns a bundle version's export record.
	Export(ctx context.Context, name, version string) (types.BundleExport, error)
	// DeleteExport removes it.
	DeleteExport(ctx context.Context, name, version string) error
}

// DistributionStore persists distribution trackers and their per-target
// jobs.
type DistributionStore interface {
	// CreateTracker stores t with its jobs and returns it with its assigned
	// ID; the jobs' TrackerID is set to it. ErrConflict when two jobs name
	// the same peer.
	CreateTracker(
		ctx context.Context, t types.DistributionTracker, jobs []types.DistributionJob,
	) (types.DistributionTracker, error)
	// Tracker returns the tracker with id.
	Tracker(ctx context.Context, id int64) (types.DistributionTracker, error)
	// UpdateTracker replaces a tracker.
	UpdateTracker(ctx context.Context, t types.DistributionTracker) error
	// Trackers returns a bundle version's trackers newest first. An empty
	// version selects every version of name, and an empty name every
	// bundle.
	Trackers(ctx context.Context, name, version string) ([]types.DistributionTracker, error)
	// PutJob creates or replaces the job (j.TrackerID, j.PeerInstanceID);
	// ErrNotFound when the tracker does not exist.
	PutJob(ctx context.Context, j types.DistributionJob) error
	// Jobs returns a tracker's jobs ordered by peer; ErrNotFound when the
	// tracker does not exist.
	Jobs(ctx context.Context, trackerID int64) ([]types.DistributionJob, error)
	// JobsForPeer returns a peer's jobs in any of statuses (all when empty),
	// ordered by (tracker, peer).
	JobsForPeer(ctx context.Context, peer string, statuses []types.JobStatus) ([]types.DistributionJob, error)
}

// ExecutionFilter narrows archive execution listings.
type ExecutionFilter struct {
	PolicyKey string
	Kind      types.ExecutionKind
}

// ArchivedFilter narrows archived item listings.
type ArchivedFilter struct {
	RepoKey     string
	PolicyKey   string
	ExecutionID int64
}

// ArchiveStore persists the cold-storage tier's records (decision 0024):
// archive policies of both families, executions, archived items and the
// cold blob index.
type ArchiveStore interface {
	// CreatePolicy stores p; ErrConflict when the key exists.
	CreatePolicy(ctx context.Context, p types.ArchivePolicy) error
	// Policy returns the policy.
	Policy(ctx context.Context, key string) (types.ArchivePolicy, error)
	// UpdatePolicy replaces the policy.
	UpdatePolicy(ctx context.Context, p types.ArchivePolicy) error
	// DeletePolicy removes the policy; its executions and items are kept.
	DeletePolicy(ctx context.Context, key string) error
	// Policies returns a family's policies ("" for both) ordered by key.
	Policies(ctx context.Context, family types.ArchiveFamily) ([]types.ArchivePolicy, error)

	// CreateExecution stores e and returns it with its assigned ID.
	CreateExecution(ctx context.Context, e types.ArchiveExecution) (types.ArchiveExecution, error)
	// Execution returns the execution with id.
	Execution(ctx context.Context, id int64) (types.ArchiveExecution, error)
	// UpdateExecution replaces an execution.
	UpdateExecution(ctx context.Context, e types.ArchiveExecution) error
	// Executions returns matching executions newest first.
	Executions(ctx context.Context, f ExecutionFilter, p Page) ([]types.ArchiveExecution, string, error)

	// AddItem stores an archived item and returns it with its assigned ID;
	// ErrInvalid when the item's full path does not split
	// (types.SplitItemPath).
	AddItem(ctx context.Context, a types.ArchivedItem) (types.ArchivedItem, error)
	// Item returns the archived item with id.
	Item(ctx context.Context, id int64) (types.ArchivedItem, error)
	// FindItems returns the archived copies of an original repository and
	// full path, newest first by ID; ErrInvalid for a path that does not
	// split.
	FindItems(ctx context.Context, repoKey, fullPath string) ([]types.ArchivedItem, error)
	// Items returns matching archived items ordered by ID; a zero
	// ExecutionID matches every execution.
	Items(ctx context.Context, f ArchivedFilter, p Page) ([]types.ArchivedItem, string, error)
	// DeleteItem removes an archived item.
	DeleteItem(ctx context.Context, id int64) error
	// ExpiredItems returns items whose ExpiresAt is set and before t,
	// oldest first, at most limit (DefaultLimit when zero).
	ExpiredItems(ctx context.Context, before time.Time, limit int) ([]types.ArchivedItem, error)

	// ColdBlobs is the cold tier's blob index.
	ColdBlobs() BlobIndex
}

// WorkerFilter narrows worker listings.
type WorkerFilter struct {
	Action      types.WorkerAction
	ProjectKey  string
	EnabledOnly bool
}

// WorkerStore persists Workers (decision 0026) with their key-value state,
// sealed secrets and execution history.
type WorkerStore interface {
	// Create stores w; ErrConflict when the key exists.
	Create(ctx context.Context, w types.Worker) error
	// Get returns the worker.
	Get(ctx context.Context, key string) (types.Worker, error)
	// Update replaces the worker.
	Update(ctx context.Context, w types.Worker) error
	// Delete removes the worker with its state, secrets and executions.
	Delete(ctx context.Context, key string) error
	// List returns matching workers ordered by key.
	List(ctx context.Context, f WorkerFilter) ([]types.Worker, error)

	// State returns a worker's state entries; ErrNotFound when the worker
	// does not exist.
	State(ctx context.Context, worker string) (map[string]string, error)
	// SetState creates or replaces one entry; ErrNotFound when the worker
	// does not exist. The per-worker limits are the caller's to apply
	// (types.ValidateWorkerState).
	SetState(ctx context.Context, worker, key, value string) error
	// DeleteState removes one entry; removing a missing entry is not an
	// error.
	DeleteState(ctx context.Context, worker, key string) error

	// PutSecret creates or replaces a secret; ErrNotFound when the worker
	// does not exist.
	PutSecret(ctx context.Context, worker, name, value string) error
	// Secrets returns a worker's secrets by name; ErrNotFound when the
	// worker does not exist.
	Secrets(ctx context.Context, worker string) (map[string]string, error)
	// DeleteSecret removes a secret.
	DeleteSecret(ctx context.Context, worker, name string) error

	// AddExecution records an execution; ErrConflict when its ID exists,
	// ErrNotFound when its worker does not.
	AddExecution(ctx context.Context, e types.WorkerExecution) error
	// Execution returns one execution.
	Execution(ctx context.Context, id string) (types.WorkerExecution, error)
	// Executions returns a worker's executions ("" for every worker) newest
	// first, by (Started, ID).
	Executions(ctx context.Context, worker string, p Page) ([]types.WorkerExecution, string, error)
	// PruneExecutions removes executions started before t and returns how
	// many.
	PruneExecutions(ctx context.Context, before time.Time) (int64, error)
}
