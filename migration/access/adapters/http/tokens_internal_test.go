package accesshttp

import (
	"testing"
)

func TestAccLegacyScope(t *testing.T) {
	for in, want := range map[string]string{
		"member-of-groups:readers,ci": "applied-permissions/groups:readers,ci",
		"member-of-groups:*":          "applied-permissions/user",
		"api:*":                       "applied-permissions/user",
		"jfrt@*:admin":                "applied-permissions/admin",
		"api:* member-of-groups:x":    "applied-permissions/user applied-permissions/groups:x",
		"applied-permissions/user":    "applied-permissions/user",
	} {
		if got := legacyScope(in); got != want {
			t.Errorf("legacyScope(%q) = %q, want %q", in, got, want)
		}
	}
}
