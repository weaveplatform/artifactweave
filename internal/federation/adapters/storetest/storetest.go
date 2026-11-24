package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
)

// Factory returns a fresh, empty federation store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the federation contract on stores from newStore, one subtest per
// concern, each on a store of its own: Replications, PushProgress, Executions, Peers, PairingNonces,
// FederationMembers, FullSyncs, AccessTargets, PlatformFederations,
// Topologies, and the stream snapshots and full-sync runs of runStreams.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runReplications(t, newStore)
	runPushProgress(t, newStore)
	runPullProgress(t, newStore)
	runExecutions(t, newStore)
	runPeers(t, newStore)
	runStreams(t, newStore)
}
