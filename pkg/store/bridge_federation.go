// Bridge (P9d): declared in migration/federation/core/port; PRO-02 deletes it.

package store

import fedport "github.com/weaveplatform/artifactweave/migration/federation/core/port"

type (
	PeerFilter       = fedport.PeerFilter
	PeerStore        = fedport.PeerStore
	FederationStore  = fedport.FederationStore
	PlatformStore    = fedport.PlatformStore
	ReplicationStore = fedport.ReplicationStore
)
