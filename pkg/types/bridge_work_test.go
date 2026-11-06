package types_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The bridge must alias the work queue's rows, not declare its own (decision
// 0044, "Type-alias bridges"): the stores build types.WorkItem, types.Lease
// and types.WorkStats values that the work.WorkQueue and work.LeaseStore
// methods return, and the queue compares an item's State with the work
// constants. A struct is assignable only to its own type, and a value of a
// named string type only to that type, so these lines compile only while each
// pair is one type.
var (
	_ work.Lease     = types.Lease{}
	_ work.WorkItem  = types.WorkItem{}
	_ work.WorkStats = types.WorkStats{}
	_ work.WorkState = types.WorkState("")
)

// A constant has no identity beyond its type and value, so the bridged states
// are compared on both: a copy whose value drifted would give items a state
// the queue does not know, and an untyped copy, though it still compiles at
// every use, is a plain string, not a WorkState, wherever it is boxed.
func TestUnitBridgeWorkStates(t *testing.T) {
	for _, c := range []struct{ legacy, target any }{
		{types.WorkPending, work.WorkPending},
		{types.WorkLeased, work.WorkLeased},
		{types.WorkParked, work.WorkParked},
	} {
		if c.legacy != c.target {
			t.Errorf("types holds %#[1]v (%[1]T), work holds %#[2]v (%[2]T)",
				c.legacy, c.target)
		}
	}
}
