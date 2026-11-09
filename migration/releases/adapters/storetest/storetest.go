package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/releases/core/port"
)

// Factory returns a fresh, empty releases store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the releases contract on stores from newStore, one subtest per
// concern, each on a store of its own: Bundles, BundleRetention,
// BundleExports and Distribution.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runBundles(t, newStore)
}
