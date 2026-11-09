// Package storetest is the contract suite for pkg/store implementations
// (decision 0015). Every backend (inmem, SQLite in memory and on disk,
// PostgreSQL) runs Run with a factory that returns a fresh, empty store, so
// one set of expectations defines the behaviour of all of them.
package storetest

import (
	"testing"

	accstoretest "github.com/weaveplatform/artifactweave/migration/access/adapters/storetest"
	accport "github.com/weaveplatform/artifactweave/migration/access/core/port"
	artstoretest "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/storetest"
	artport "github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	fedstoretest "github.com/weaveplatform/artifactweave/migration/federation/adapters/storetest"
	fedport "github.com/weaveplatform/artifactweave/migration/federation/core/port"
	ntfstoretest "github.com/weaveplatform/artifactweave/migration/notifications/adapters/storetest"
	ntfport "github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	platstoretest "github.com/weaveplatform/artifactweave/migration/platform/store/storetest"
	relstoretest "github.com/weaveplatform/artifactweave/migration/releases/adapters/storetest"
	relport "github.com/weaveplatform/artifactweave/migration/releases/core/port"
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
	artstoretest.Run(t, func(t *testing.T) artport.Store { return newStore(t) })
	// A layout document lives in the platform's config store and fences the
	// artifacts module's remote sources, so this test needs the whole
	// aggregate and is not part of the artifacts contract.
	t.Run("RemoteLayoutFences", func(t *testing.T) { testRemoteLayoutFences(t, newStore(t)) })
	accstoretest.Run(t, func(t *testing.T) accport.Store { return newStore(t) })
	ntfstoretest.Run(t, func(t *testing.T) ntfport.Store { return newStore(t) })
	retstoretest.Run(t, func(t *testing.T) retport.Store { return newStore(t) })
	fedstoretest.Run(t, func(t *testing.T) fedport.Store { return newStore(t) })
	platstoretest.RunWithoutWatches(
		t,
		func(t *testing.T) platstoretest.Store { return newStore(t) },
	)
	relstoretest.Run(t, func(t *testing.T) relport.Store { return newStore(t) })
	wrkstoretest.Run(t, func(t *testing.T) wrkport.Store { return newStore(t) })
	runBuildRowLock(t, newStore)
}
