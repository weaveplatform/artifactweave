package port

import "github.com/weaveplatform/artifactweave/internal/platform/tx"

// ConfigurationStore is the unit of work and replication catalogue needed by
// single-target configuration, independently of peer and federation services.
type ConfigurationStore interface {
	tx.Runner
	Replications() ReplicationStore
}

// Store is the part of the metadata store the federation module uses:
// replications, peers, federated repository membership and full syncs, and
// the platform federation records, with the shared unit of work
// (tx.Runner). Stream positions are assigned in the transaction of the
// change they record (decision 0021), so the federation stores run inside
// that unit of work rather than one of their own.
type Store interface {
	PushStore
	Peers() PeerStore
	Federation() FederationStore
	Platform() PlatformStore
}
