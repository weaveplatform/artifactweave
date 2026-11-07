package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The bridge must alias the replay port and its values, not declare its own
// (decision 0044, "Type-alias bridges"): the SQL and in-memory stores
// implement the port under the store names, and the metadata store's Replays
// returns it as store.ReplayStore, while the HTTP boundary and the build
// service build store.ReplayRequest values for it, and neither side converts.
// A declared ReplayStore would still compile here, because interfaces with the
// same methods are assignable both ways. A struct is assignable only to its own
// type and a pointer only to a pointer to the identical type, so these lines
// compile only while each pair is one type.
var (
	_ idempotency.ReplayRequest = store.ReplayRequest{}
	_ idempotency.ReplayReceipt = store.ReplayReceipt{}
	_ *idempotency.ReplayStore  = (*store.ReplayStore)(nil)
)

// If the bridged sentinel were a copy, it would still compile and print the
// same text, but errors.Is would stop matching between the stores, which
// return it, and the HTTP boundary, which answers a reused key with different
// request bytes with 422 when it matches. Only the same value matches in both
// directions, so that is what the test checks, also through a wrap.
func TestUnitBridgeIdempotencySentinel(t *testing.T) {
	legacy, target := store.ErrReplayMismatch, idempotency.ErrReplayMismatch
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("the store and idempotency names hold different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped sentinel does not match under both names")
	}
	const text = "store: idempotency key has different intent"
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != text {
			t.Errorf("Error() = %q, want %q", got, text)
		}
	}
}
