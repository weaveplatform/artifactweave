package api_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

// TestUnitFoldUsername: user names fold to lower case and match in any case
// (#1432), the rule access/core/model and Conan's login both apply.
func TestUnitFoldUsername(t *testing.T) {
	if got := api.FoldUsername("Bob.Smith@Example"); got != "bob.smith@example" {
		t.Errorf("FoldUsername = %q", got)
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"BOB", "bob", true},
		{"Bob", "bOb", true},
		{"bob", "bob", true},
		{"bob", "bobby", false},
		{"bob", "", false},
	} {
		if got := api.SameUsername(tc.a, tc.b); got != tc.want {
			t.Errorf("SameUsername(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
