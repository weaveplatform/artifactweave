package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitPasswordExpired(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	on := types.PasswordExpirationPolicy{Enabled: true, MaxAgeDays: 30}
	for name, c := range map[string]struct {
		u        types.User
		p        types.PasswordExpirationPolicy
		expected bool
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
		if got := PasswordExpired(c.u, c.p, now); got != c.expected {
			t.Errorf("%s: %v, want %v", name, got, c.expected)
		}
	}
}

func TestUnitLockoutThroughBasic(t *testing.T) {
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
	// The lock answers 403 whatever the password, before it is checked, and
	// counts nothing.
	for _, pw := range []string{"pw", "wrong"} {
		if _, err := a.Basic(ctx, "dev", pw); !errors.Is(err, ErrAccountLocked) {
			t.Fatalf("a locked user with %q: %v", pw, err)
		}
	}
	if u, _ := db.Users().Get(ctx, "dev"); u.FailedLogins != 2 {
		t.Fatalf("the locked logins were counted: %d", u.FailedLogins)
	}
	if err := db.Users().Unlock(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("after unlocking: %v", err)
	}
}

// TestUnitLockBeforeSuspensionAndPassword: a locked account answers its 403 before
// the suspension and before any password is hashed, so a suspended locked
// account still reads as locked, and with every hashing slot taken the
// answer still comes at once (#475).
func TestUnitLockBeforeSuspensionAndPassword(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev", Status: types.UserEnabled}, "pw")
	a, _ := newAuth(t, db, Options{Concurrency: 1})
	for range 2 {
		_, _ = a.Basic(ctx, "dev", "wrong")
	}
	if err := ezBasic(a, "dev", "pw"); !errors.Is(err, ErrLoginSuspended) {
		t.Fatalf("not suspended: %v", err)
	}
	u, err := db.Users().Get(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	u.Status = types.UserLocked
	if err := db.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	a.slots <- struct{}{}
	defer func() { <-a.slots }()
	// The store still answers, but a hash would wait for a slot until the
	// deadline and fail with it.
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, pw := range []string{"pw", "wrong"} {
		_, err := a.Basic(bounded, "dev", pw)
		var e *apierror.Error
		if !errors.Is(err, ErrAccountLocked) || !errors.As(err, &e) ||
			e.Status != http.StatusForbidden {
			t.Fatalf("%s while locked and suspended: %v", pw, err)
		}
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

func TestUnitExpiryThroughBasic(t *testing.T) {
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

func TestUnitPolicyReadErrors(t *testing.T) {
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
