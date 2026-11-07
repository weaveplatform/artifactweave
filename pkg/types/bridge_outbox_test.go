package types_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/outbox"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The bridge must alias the clock and the outbox values, not declare its own
// (decision 0044, "Type-alias bridges"): the SQL and in-memory stores build
// types.OutboxEntry, types.PeerCursor, types.AppliedMarker and
// types.FailedBlobFetch values that the outbox.OutboxStore methods take and
// return, Stamp holds its time as a types.HLC and orders it with Compare, and
// neither side converts. A struct is assignable only to its own type, so these
// lines compile only while each pair is one type.
var (
	_ outbox.HLC             = types.HLC{}
	_ outbox.OutboxEntry     = types.OutboxEntry{}
	_ outbox.PeerCursor      = types.PeerCursor{}
	_ outbox.AppliedMarker   = types.AppliedMarker{}
	_ outbox.FailedBlobFetch = types.FailedBlobFetch{}
)

// If the bridged sentinel were a copy, it would print the same text, but
// errors.Is would stop matching between callers that test the old name and
// ParseHLC, which wraps outbox's value. Only the same value matches in both
// directions, so that is what the test checks, also through a wrap and on an
// error that ParseHLC returns through the bridge.
func TestUnitBridgeOutboxSentinel(t *testing.T) {
	legacy, target := types.ErrInvalidHLC, outbox.ErrInvalidHLC
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("types.ErrInvalidHLC and outbox.ErrInvalidHLC are different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped ErrInvalidHLC does not match under both names")
	}
	_, err := types.ParseHLC("1.2")
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("types.ParseHLC: %v does not match the sentinel under both names", err)
	}
	const text = "invalid hybrid logical clock value"
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != text {
			t.Errorf("ErrInvalidHLC.Error() = %q, want %q", got, text)
		}
	}
}

// The text form sorts as Compare does, which is what lets it serve as an
// indexed column, so there must be one parser of it. A copy of the function,
// even one with the same body, would leave two that could drift apart
// unnoticed while the old name remains, so both names must hold the outbox
// function and answer alike.
func TestUnitBridgeOutboxParser(t *testing.T) {
	if reflect.ValueOf(types.ParseHLC).Pointer() != reflect.ValueOf(outbox.ParseHLC).Pointer() {
		t.Error("types.ParseHLC is not outbox.ParseHLC")
	}
	for _, s := range []string{"0000000000000001.00000002", "1.2", ""} {
		legacy, legacyErr := types.ParseHLC(s)
		target, targetErr := outbox.ParseHLC(s)
		if legacy != target || fmt.Sprint(legacyErr) != fmt.Sprint(targetErr) {
			t.Errorf("%q: %+v, %v under the types name; %+v, %v under the outbox name",
				s, legacy, legacyErr, target, targetErr)
		}
	}
}
