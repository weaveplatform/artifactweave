package types_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/instance"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The bridge must alias the identity, not declare its own (decision 0044,
// "Type-alias bridges"): the stores build types.InstanceIdentity values that
// the instance.InstanceStore methods take and return, and neither side
// converts. A struct is assignable only to its own type, so this line compiles
// only while the pair is one type.
var _ instance.InstanceIdentity = types.InstanceIdentity{}

// If the bridged sentinel were a copy, it would print the same text, but
// errors.Is would stop matching between callers that test the old name and
// ValidateInstanceID, which wraps instance's value. Only the same value
// matches in both directions, so that is what the test checks, also through a
// wrap and on an error that ValidateInstanceID returns through the bridge.
func TestUnitBridgeInstanceSentinel(t *testing.T) {
	legacy, target := types.ErrInvalidInstanceID, instance.ErrInvalidInstanceID
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("types.ErrInvalidInstanceID and instance.ErrInvalidInstanceID are different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped ErrInvalidInstanceID does not match under both names")
	}
	err := types.ValidateInstanceID("jfrt@x")
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("types.ValidateInstanceID: %v does not match the sentinel under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "invalid instance id" {
			t.Errorf("ErrInvalidInstanceID.Error() = %q, want %q", got, "invalid instance id")
		}
	}
}

// Callers that check an id through the old name, such as the composition
// root's runtime test, keep doing so until PRO-01 re-points them. A copy of
// the function, even one with the same body, would leave two sets of rules
// that could drift apart unnoticed, so both names must hold the instance
// function and answer alike.
func TestUnitBridgeInstanceValidator(t *testing.T) {
	if reflect.ValueOf(types.ValidateInstanceID).Pointer() !=
		reflect.ValueOf(instance.ValidateInstanceID).Pointer() {
		t.Error("types.ValidateInstanceID is not instance.ValidateInstanceID")
	}
	for _, id := range []string{"jfrt@0f8fad5b-d9cb-469f-a165-70867728950e", "jfrt@x", ""} {
		legacy, target := types.ValidateInstanceID(id), instance.ValidateInstanceID(id)
		if fmt.Sprint(legacy) != fmt.Sprint(target) {
			t.Errorf("%q: %v under the types name, %v under the instance name", id, legacy, target)
		}
	}
}

// A constant has no identity beyond its type and value, so the bridged
// protocol version is compared on both. Every stored identity records it, and
// peers advertise it at pairing (decision 0021): a copy that drifted would
// make the two names disagree on which protocol this build speaks.
func TestUnitBridgeInstanceProtocolVersion(t *testing.T) {
	var legacy, target any = types.ProtocolVersion, instance.ProtocolVersion
	if legacy != target {
		t.Errorf("types holds %#[1]v (%[1]T), instance holds %#[2]v (%[2]T)", legacy, target)
	}
}
