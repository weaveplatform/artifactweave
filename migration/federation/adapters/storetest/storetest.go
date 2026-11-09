package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
)

// Factory returns a fresh, empty federation store and registers the store's
// cleanup with t.
type Factory func(t *testing.T) port.Store

// Run runs the federation contract on stores from newStore, one subtest per
// concern, each on a store of its own: Replications, Peers, PairingNonces,
// FederationMembers, FullSyncs, AccessTargets, PlatformFederations and
// Topologies.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runReplications(t, newStore)
	runPeers(t, newStore)
}
