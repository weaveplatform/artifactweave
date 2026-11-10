package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/workers/core/port"
)

// Factory returns a fresh, empty workers store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the workers contract on stores from newStore, one subtest per
// concern, each on a store of its own: Workers, WorkerState, WorkerSecrets
// and WorkerExecutions.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runWorkers(t, newStore)
}
