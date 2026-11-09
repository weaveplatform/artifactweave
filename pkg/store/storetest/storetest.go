// Package storetest is the contract suite for pkg/store implementations
// (decision 0015). Every backend (inmem, SQLite in memory and on disk,
// PostgreSQL) runs Run with a factory that returns a fresh, empty store, so
// one set of expectations defines the behaviour of all of them.
package storetest

import (
	"testing"

	platstoretest "github.com/weaveplatform/artifactweave/migration/platform/store/storetest"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Factory returns a fresh, empty store and registers its cleanup with t.
type Factory func(t *testing.T) store.Store

// Run executes the full contract, one subtest per concern, each on its own
// store.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runCore(t, newStore)
	runBlobs(t, newStore)
	runReplicas(t, newStore)
	runContent(t, newStore)
	runUploads(t, newStore)
	runGitLFSLocks(t, newStore)
	runBowerRegistrations(t, newStore)
	runRemoteCache(t, newStore)
	runIdentity(t, newStore)
	runProjects(t, newStore)
	runWebhooks(t, newStore)
	runCleanup(t, newStore)
	runReplications(t, newStore)
	platstoretest.RunWithoutWatches(
		t,
		func(t *testing.T) platstoretest.Store { return newStore(t) },
	)
	runPeers(t, newStore)
	runOutbox(t, newStore)
	runBundles(t, newStore)
	runArchive(t, newStore)
	runArchivedItems(t, newStore)
	runWorkers(t, newStore)
	runBuilds(t, newStore)
	runReplays(t, newStore)
	runBuildRowLock(t, newStore)
	runRequestBudgets(t, newStore)
	runAPIRestrictions(t, newStore)
}
