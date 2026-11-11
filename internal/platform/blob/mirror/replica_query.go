package mirror

// ReplicaFilter narrows replica listings; empty fields match everything.
type ReplicaFilter struct {
	Member string
	States []ReplicaState
}

// ReplicaCount is one member's number of replicas in one state and their
// combined size, for the storage summary.
type ReplicaCount struct {
	Member string
	State  ReplicaState
	Count  int64
	Bytes  int64
}
