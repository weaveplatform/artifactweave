package auth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handlers test errors.Is against these sentinels under the auth names while
// the kernel's RequireAuthenticated and RequireAdmin return the kernel's
// values. A copied sentinel would print the same text with the same status,
// but the two names would stop matching, so the test checks that each auth
// name is the kernel's value, also through a wrap and on an error that the
// authenticator returns, and that the status and message JFrog clients see
// are unchanged. The authenticator lives in access/core/authn, so the test
// builds one through that package's exported API, with a cheap hash.
func TestUnitBridgeKernelAuthSentinels(t *testing.T) {
	const unauthorized, forbidden = http.StatusUnauthorized, http.StatusForbidden
	cases := []struct {
		legacy, kernel error
		status         int
		message        string
	}{
		{ErrBadCredentials, kernel.ErrBadCredentials, unauthorized, "Bad credentials"},
		{ErrUnauthenticated, kernel.ErrUnauthenticated, unauthorized, "Authentication is required"},
		{ErrForbidden, kernel.ErrForbidden, forbidden, "Forbidden"},
		{
			ErrAPIRestricted, kernel.ErrAPIRestricted, forbidden,
			"API access is temporarily restricted",
		},
		{ErrPasswordExpired, kernel.ErrPasswordExpired, forbidden, "Password has expired"},
		{
			ErrAccountLocked, kernel.ErrAccountLocked, forbidden,
			"User is Locked.\nContact System Administrator to Unlock The Account.",
		},
	}
	for _, c := range cases {
		t.Run(c.message, func(t *testing.T) {
			if !errors.Is(c.legacy, c.kernel) || !errors.Is(c.kernel, c.legacy) {
				t.Error("the auth and kernel names hold different errors")
			}
			if !errors.Is(fmt.Errorf("op: %w", c.kernel), c.legacy) ||
				!errors.Is(fmt.Errorf("op: %w", c.legacy), c.kernel) {
				t.Error("a wrapped sentinel does not match under both names")
			}
			for _, err := range []error{c.legacy, c.kernel} {
				var e *apierror.Error
				if !errors.As(err, &e) || e.Status != c.status || e.Message != c.message {
					t.Errorf("%q: status and message %+v, want %d %q", err, e, c.status, c.message)
				}
			}
		})
	}

	cheap := authn.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}
	hash, err := authn.HashPasswordWith(cheap, "password")
	if err != nil {
		t.Fatal(err)
	}
	db := inmem.New()
	if err := db.Users().Create(t.Context(), types.User{Name: "ann", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: cheap})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Basic(t.Context(), "ann", "wrong")
	if !errors.Is(err, ErrBadCredentials) || !errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("a wrong password: %v does not match ErrBadCredentials under both names", err)
	}
}
