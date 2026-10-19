package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestPasswordExpired(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	on := types.PasswordExpirationPolicy{Enabled: true, MaxAgeDays: 30}
	for name, c := range map[string]struct {
		u    types.User
		p    types.PasswordExpirationPolicy
		want bool
	}{
		"forced, policy off": {types.User{PasswordExpired: true}, types.PasswordExpirationPolicy{}, true},
		"young":              {types.User{PasswordChanged: now.AddDate(0, 0, -29)}, on, false},
		"old":                {types.User{PasswordChanged: now.AddDate(0, 0, -31)}, on, true},
		"old, policy off":    {types.User{PasswordChanged: now.AddDate(0, 0, -31)}, types.PasswordExpirationPolicy{}, false},
		"age unknown":        {types.User{}, on, false},
		"no max age": {
			types.User{PasswordChanged: now.AddDate(-1, 0, 0)},
			types.PasswordExpirationPolicy{Enabled: true},
			false,
		},
	} {
		if got := PasswordExpired(c.u, c.p, now); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestLockoutThroughBasic(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev", Status: types.UserEnabled}, "pw")
	noSuspension(t, db)
	a, c := newAuth(t, db, Options{})

	// With the policy off, failures are not counted.
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatal(err)
	}
	if u, _ := db.Users().Get(ctx, "dev"); u.FailedLogins != 0 {
		t.Fatalf("counted with the policy off: %d", u.FailedLogins)
	}

	if err := db.Settings().Put(ctx, types.SettingUserLockPolicy,
		types.UserLockPolicy{Enabled: true, LoginAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	// The cached (off) policy still applies until it expires or is forgotten.
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatal(err)
	}
	if u, _ := db.Users().Get(ctx, "dev"); u.FailedLogins != 0 {
		t.Fatalf("the cached policy was not used: %d", u.FailedLogins)
	}
	c.t = c.t.Add(policyTTL)
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatal(err)
	}
	// A correct password between failures resets the count, even from the
	// verified-credentials cache.
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatal(err)
	}
	if u, _ := db.Users().Get(ctx, "dev"); u.FailedLogins != 0 {
		t.Fatalf("a login did not reset the count: %d", u.FailedLogins)
	}
	for range 2 {
		_, _ = a.Basic(ctx, "dev", "wrong")
	}
	if _, err := a.Basic(ctx, "dev", "pw"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a locked user signed in: %v", err)
	}
	if err := db.Users().Unlock(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("after unlocking: %v", err)
	}
}

// noSuspension turns the temporary login suspension off, so a test of the
// user lock, the other defence, sees its own answers alone.
func noSuspension(t *testing.T, db *inmem.Store) {
	t.Helper()
	s := types.DefaultAccessLockSettings()
	s.ClientLoginSuspensionEnabled = false
	if err := db.Settings().
		Put(context.Background(), types.SettingAccessLockSettings, s); err != nil {
		t.Fatal(err)
	}
}

func TestExpiryThroughBasic(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	a, c := newAuth(t, db, Options{})
	addUser(t, db, types.User{Name: "dev", PasswordChanged: c.t.AddDate(0, 0, -10)}, "pw")
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := db.Settings().Put(ctx, types.SettingPasswordExpirationPolicy,
		types.PasswordExpirationPolicy{Enabled: true, MaxAgeDays: 5}); err != nil {
		t.Fatal(err)
	}
	a.ForgetPolicies()
	// The cached credentials do not bypass the policy.
	if _, err := a.Basic(ctx, "dev", "pw"); !errors.Is(err, ErrPasswordExpired) {
		t.Fatalf("an old password: %v", err)
	}
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a wrong password must not learn of the expiry: %v", err)
	}
}

func TestPolicyReadErrors(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev"}, "pw")
	a, err := NewAuthenticator(db.Users(), failingSettings{db.Settings()}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a policy that cannot be read: %v", err)
	}
	// A wrong password still answers as one: counting is best effort.
	if _, err := a.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a wrong password with unreadable policies: %v", err)
	}
	if _, err := LockPolicy(ctx, failingSettings{db.Settings()}); err == nil {
		t.Fatal("LockPolicy hid the read error")
	}
	if _, err := ExpirationPolicy(ctx, failingSettings{db.Settings()}); err == nil {
		t.Fatal("ExpirationPolicy hid the read error")
	}
	if _, err := AccessLockSettings(ctx, failingSettings{db.Settings()}); err == nil {
		t.Fatal("AccessLockSettings hid the read error")
	}
}
