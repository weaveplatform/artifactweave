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
