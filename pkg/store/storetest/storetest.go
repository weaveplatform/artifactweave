// Package storetest is the contract suite for pkg/store implementations
// (decision 0015). Every backend (inmem, SQLite in memory and on disk,
// PostgreSQL) runs Run with a factory that returns a fresh, empty store, so
// one set of expectations defines the behaviour of all of them.
package storetest

import (
	"testing"

	ntfstoretest "github.com/weaveplatform/artifactweave/migration/notifications/adapters/storetest"
	ntfport "github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	platstoretest "github.com/weaveplatform/artifactweave/migration/platform/store/storetest"
	retstoretest "github.com/weaveplatform/artifactweave/migration/retention/adapters/storetest"
	retport "github.com/weaveplatform/artifactweave/migration/retention/core/port"
	wrkstoretest "github.com/weaveplatform/artifactweave/migration/workers/adapters/storetest"
	wrkport "github.com/weaveplatform/artifactweave/migration/workers/core/port"
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
	runGitLFSLocks(t, newStore)
	runBowerRegistrations(t, newStore)
	runRemoteCache(t, newStore)
	runIdentity(t, newStore)
	runProjects(t, newStore)
	ntfstoretest.Run(t, func(t *testing.T) ntfport.Store { return newStore(t) })
	retstoretest.Run(t, func(t *testing.T) retport.Store { return newStore(t) })
	runReplications(t, newStore)
	platstoretest.RunWithoutWatches(
		t,
		func(t *testing.T) platstoretest.Store { return newStore(t) },
	)
	runPeers(t, newStore)
	runBundles(t, newStore)
	runArchivedItems(t, newStore)
	wrkstoretest.Run(t, func(t *testing.T) wrkport.Store { return newStore(t) })
	runBuilds(t, newStore)
	runBuildRowLock(t, newStore)
	runAPIRestrictions(t, newStore)
}
