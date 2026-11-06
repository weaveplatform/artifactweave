package types_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The bridge must alias the mirror's replica rows, not declare its own
// (decision 0044, "Type-alias bridges"): the SQL and in-memory replica indexes
// take and return types.BlobReplica values in the methods the mirror calls
// through its ReplicaIndex port, and the mirror compares a record's State with
// its own constants. A struct is assignable only to its own type, and a value
// of a named string type only to that type, so these lines compile only while
// each pair is one type.
var (
	_ mirror.BlobReplica  = types.BlobReplica{}
	_ mirror.ReplicaState = types.ReplicaState("")
)

// A constant has no identity beyond its type and value, so the bridged states
// are compared on both: a copy whose value drifted would leave the in-memory
// replica index and the storage summary, which use the types names, blind to
// the states the mirror writes, and an untyped copy, though it still compiles
// at every use, is a plain string, not a ReplicaState, wherever it is boxed.
func TestUnitBridgeMirrorReplicaStates(t *testing.T) {
	for _, c := range []struct{ legacy, target any }{
		{types.ReplicaPresent, mirror.ReplicaPresent},
		{types.ReplicaPending, mirror.ReplicaPending},
		{types.ReplicaDeleting, mirror.ReplicaDeleting},
		{types.ReplicaFailed, mirror.ReplicaFailed},
	} {
		if c.legacy != c.target {
			t.Errorf("types holds %#[1]v (%[1]T), mirror holds %#[2]v (%[2]T)",
				c.legacy, c.target)
		}
	}
}
