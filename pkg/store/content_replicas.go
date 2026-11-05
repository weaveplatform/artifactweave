package store

import "github.com/weaveplatform/artifactweave/pkg/types"

// ReplicaFilter narrows replica listings; empty fields match everything.
type ReplicaFilter struct {
	Member string
	States []types.ReplicaState
}

// ReplicaCount is one member's number of replicas in one state and their
// combined size, for the storage summary.
type ReplicaCount struct {
	Member string
	State  types.ReplicaState
	Count  int64
	Bytes  int64
}
