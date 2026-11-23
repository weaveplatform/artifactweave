package api_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitUserInfoEnabled(t *testing.T) {
	cases := []struct {
		name string
		user api.UserInfo
		want bool
	}{
		{"no status", api.UserInfo{Name: "alice"}, true},
		{"enabled", api.UserInfo{Name: "alice", Status: "enabled"}, true},
		{"disabled", api.UserInfo{Name: "alice", Status: "disabled"}, false},
		{"locked", api.UserInfo{Name: "alice", Status: "locked"}, false},
		{"invited", api.UserInfo{Name: "alice", Status: "invited"}, false},
		// The legacy view's upper-case spelling is never stored, and the
		// comparison is exact, as access's own checks are.
		{"legacy spelling", api.UserInfo{Name: "alice", Status: "ENABLED"}, false},
		// Being an administrator does not enable a disabled account.
		{"disabled admin", api.UserInfo{Name: "root", Admin: true, Status: "disabled"}, false},
	}
	for _, c := range cases {
		if got := c.user.Enabled(); got != c.want {
			t.Errorf("%s: Enabled() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUnitSecurityPolicyInvalid(t *testing.T) {
	err := error(&api.SecurityPolicyInvalid{Section: "userLockPolicy", Reason: "loginAttempts must be at least 1"})
	if err.Error() != "security.userLockPolicy: loginAttempts must be at least 1" || !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal(err)
	}
}
