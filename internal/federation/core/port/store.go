package port

import "github.com/weaveplatform/artifactweave/internal/platform/tx"

// Store is the part of the metadata store the federation module uses:
// replications, peers, federated repository membership and full syncs, and
// the platform federation records, with the shared unit of work
// (tx.Runner). Stream positions are assigned in the transaction of the
// change they record (decision 0021), so the federation stores run inside
// that unit of work rather than one of their own.
type Store interface {
	tx.Runner
	Replications() ReplicationStore
	Peers() PeerStore
	Federation() FederationStore
	Platform() PlatformStore
}
