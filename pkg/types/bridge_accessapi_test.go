package types_test

import (
	"errors"
	"fmt"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A copied sentinel would print the same text, but the permission
// conversions that test errors.Is against the old name would stop matching
// the errors the action parsers return, which wrap access/api's value. Only
// the same value matches under both names, so that is what the test checks,
// also on an error that a parser returns through the bridge.
func TestUnitBridgeAccessAPIActionSentinel(t *testing.T) {
	legacy, target := types.ErrUnknownAction, accessapi.ErrUnknownAction
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("types.ErrUnknownAction and accessapi.ErrUnknownAction are different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped ErrUnknownAction does not match under both names")
	}
	_, err := types.ParseLetters([]string{"q"})
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("types.ParseLetters error %v does not match the sentinel under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "unknown permission action" {
			t.Errorf("ErrUnknownAction.Error() = %q, want %q", got, "unknown permission action")
		}
	}
}
