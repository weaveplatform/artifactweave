package kernel_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

func TestUnitSplitItemPath(t *testing.T) {
	cases := []struct {
		in           string
		parent, name string
		depth        int
	}{
		{"a.jar", ".", "a.jar", 1},
		{"/org/acme/a.jar", "org/acme", "a.jar", 3},
		{"org/acme/", "org", "acme", 2},
	}
	for _, c := range cases {
		parent, name, depth, err := kernel.SplitItemPath(c.in)
		if err != nil || parent != c.parent || name != c.name || depth != c.depth {
			t.Errorf("%q = %q %q %d %v", c.in, parent, name, depth, err)
		}
	}
	for _, bad := range []string{"", "/", "a//b", "a/./b", "../x", "a/..", `a\b`, "a\x00b"} {
		if _, _, _, err := kernel.SplitItemPath(bad); !errors.Is(err, kernel.ErrInvalidItemPath) {
			t.Errorf("%q: want ErrInvalidItemPath, got %v", bad, err)
		}
	}
}
