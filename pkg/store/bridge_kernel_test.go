package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// If a bridged sentinel were a copy, it would still compile and print the same
// text, but errors.Is would stop matching between callers that use the old
// name and adapters that return the kernel's value. Only the same value
// matches in both directions, so that is what the test checks, also through a
// wrap.
func TestUnitBridgeKernelSentinels(t *testing.T) {
	cases := []struct {
		legacy, kernel error
		text           string
	}{
		{store.ErrNotFound, kernel.ErrNotFound, "store: not found"},
		{store.ErrConflict, kernel.ErrConflict, "store: conflict"},
		{store.ErrInvalidCursor, kernel.ErrInvalidCursor, "store: invalid cursor"},
		{store.ErrInvalid, kernel.ErrInvalid, "store: invalid record"},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			if !errors.Is(c.legacy, c.kernel) || !errors.Is(c.kernel, c.legacy) {
				t.Error("the store and kernel names hold different errors")
			}
			if !errors.Is(fmt.Errorf("op: %w", c.kernel), c.legacy) ||
				!errors.Is(fmt.Errorf("op: %w", c.legacy), c.kernel) {
				t.Error("a wrapped sentinel does not match under both names")
			}
			for _, err := range []error{c.legacy, c.kernel} {
				if got := err.Error(); got != c.text {
					t.Errorf("Error() = %q, want %q", got, c.text)
				}
			}
		})
	}
}
