package port

import (
	"context"

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
	ReplaceArtifacts(
		ctx context.Context,
		name, version string,
		artifacts []types.BundleArtifact,
	) error
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
	JobsForPeer(
		ctx context.Context,
		peer string,
		statuses []types.JobStatus,
	) ([]types.DistributionJob, error)
}
