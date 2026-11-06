package types_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// If the bridged sentinel were a copy, it would print the same text, but
// errors.Is would stop matching between callers that use the old name and
// SplitItemPath, which wraps the kernel's value. Only the same value matches
// in both directions, so that is what the test checks, also through a wrap.
func TestUnitBridgeKernelItemPathSentinel(t *testing.T) {
	legacy, target := types.ErrInvalidItemPath, kernel.ErrInvalidItemPath
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("types.ErrInvalidItemPath and kernel.ErrInvalidItemPath are different errors")
	}
	if !errors.Is(fmt.Errorf("op: %w", target), legacy) ||
		!errors.Is(fmt.Errorf("op: %w", legacy), target) {
		t.Error("a wrapped ErrInvalidItemPath does not match under both names")
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "invalid item path" {
			t.Errorf("ErrInvalidItemPath.Error() = %q, want %q", got, "invalid item path")
		}
	}
}

// A copied sentinel would print the same text, but an error from ParseScope,
// which wraps the kernel's value, would stop matching the old name. Only the
// same value matches under both names, so that is what the test checks, also
// on an error that ParseScope returns through the bridge.
func TestUnitBridgeKernelScopeSentinel(t *testing.T) {
	legacy, target := types.ErrInvalidScope, kernel.ErrInvalidScope
	if !errors.Is(legacy, target) || !errors.Is(target, legacy) {
		t.Error("types.ErrInvalidScope and kernel.ErrInvalidScope are different errors")
	}
	_, err := types.ParseScope("widget:x:r")
	if !errors.Is(err, legacy) || !errors.Is(err, target) {
		t.Errorf("types.ParseScope error %v does not match ErrInvalidScope under both names", err)
	}
	for _, err := range []error{legacy, target} {
		if got := err.Error(); got != "invalid token scope" {
			t.Errorf("ErrInvalidScope.Error() = %q, want %q", got, "invalid token scope")
		}
	}
}
