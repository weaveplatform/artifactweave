package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// Factory returns a fresh, empty artifacts store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the artifacts contract on stores from newStore, one subtest per
// concern, each on a store of its own: Blobs, ColdBlobs, BlobDeletion,
// ColdBlobDeletion, BlobTiersSeparate, Replicas, ReplicaTiersSeparate, Repos,
// Items, ItemCreate, ColonPaths, ItemProperties, RepositoryRoot, ItemStats,
// ItemSearch, LongPropertyValues, LongPropertyKeys, Trash, Packages,
// GitLFSLocks, BowerRegistrations, RemoteCache, RemoteCacheFences,
// RemoteCacheAtomic, RemoteCacheEvict, RemoteCacheConcurrent,
// RemoteCacheUsage, RemoteSmartState, ArchivedItems, Builds, BuildRuns,
// BuildDeletes, BuildRename, BuildPromotions, BuildFiles, BuildRetention and
// BuildReports.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runBlobs(t, newStore)
	runReplicas(t, newStore)
	runContent(t, newStore)
	runGitLFSLocks(t, newStore)
	runBowerRegistrations(t, newStore)
	runRemoteCache(t, newStore)
	runArchivedItems(t, newStore)
	runBuilds(t, newStore)
}
