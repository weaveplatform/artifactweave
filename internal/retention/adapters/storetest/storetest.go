package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// Factory returns a fresh, empty retention store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the retention contract on stores from newStore, one subtest per
// concern, each on a store of its own: CleanupPolicies, CleanupRuns,
// ArchivePolicies and ArchiveExecutions.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runCleanup(t, newStore)
	runArchive(t, newStore)
}
