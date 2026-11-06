package store_test

import (
	"github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The bridge must alias the mirror's replica query types, not declare its own
// (decision 0044, "Type-alias bridges"): the SQL and in-memory replica indexes
// take a store.ReplicaFilter in List and return store.ReplicaCount values from
// Counts, which the mirror calls through its ReplicaIndex port with the mirror
// names, and neither side converts. A struct is assignable only to its own
// type, so these lines compile only while each pair is one type.
var (
	_ mirror.ReplicaFilter = store.ReplicaFilter{}
	_ mirror.ReplicaCount  = store.ReplicaCount{}
)
