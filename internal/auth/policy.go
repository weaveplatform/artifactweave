package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The policies' defaults when nothing is stored, as Artifactory ships them.
const (
	DefaultLoginAttempts      = 5
	DefaultPasswordMaxAgeDays = 60
)

// policyTTL is how long the authenticator reuses the stored policies. Every
// Basic login needs them, and a settings read per request would put the
// database on the hot path; a policy changed on another instance takes
// effect here within this long.
const policyTTL = 10 * time.Second

// ErrPasswordExpired answers a correct password the expiration policy, or
// an administrator, has expired. It is reported only after the password
// verifies, so it tells nothing to someone without it.
var ErrPasswordExpired = apierror.New(http.StatusUnauthorized,
	"The password has expired; set a new one with PUT /access/api/v2/users/{username}/password")

// ErrBasicAuthDisabled answers a user name and password while the platform
// accepts only tokens (basic_authentication_enabled is false).
var ErrBasicAuthDisabled = apierror.New(http.StatusUnauthorized,
	"Basic authentication with a password is disabled; use an access token")

// BasicAuthEnabled reports whether user names and passwords are accepted
// over HTTP Basic. Unset means on, as Artifactory ships.
func BasicAuthEnabled(ctx context.Context, settings store.SettingsStore) (bool, error) {
	on := true
	if err := settings.Get(ctx, types.SettingBasicAuthEnabled, &on); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return on, fmt.Errorf("auth: basic authentication switch: %w", err)
	}
	return on, nil
}

// LockPolicy returns the stored user lock policy, or the default.
func LockPolicy(ctx context.Context, settings store.SettingsStore) (types.UserLockPolicy, error) {
	p := types.UserLockPolicy{LoginAttempts: DefaultLoginAttempts}
	if err := settings.Get(ctx, types.SettingUserLockPolicy, &p); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return p, fmt.Errorf("auth: user lock policy: %w", err)
	}
	return p, nil
}

// ExpirationPolicy returns the stored password expiration policy, or the
// default.
func ExpirationPolicy(
	ctx context.Context,
	settings store.SettingsStore,
) (types.PasswordExpirationPolicy, error) {
	p := types.PasswordExpirationPolicy{MaxAgeDays: DefaultPasswordMaxAgeDays, NotifyByEmail: true}
	if err := settings.Get(ctx, types.SettingPasswordExpirationPolicy, &p); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return p, fmt.Errorf("auth: password expiration policy: %w", err)
	}
	return p, nil
}

// PasswordExpired reports whether u must set a new password. An
// administrator's expiry applies whatever the policy; age counts only from
// a recorded change, so users whose password predates the record (the
// seeded admin of an older install) are not locked out by enabling it.
func PasswordExpired(u types.User, p types.PasswordExpirationPolicy, now time.Time) bool {
	if u.PasswordExpired {
		return true
	}
	if !p.Enabled || p.MaxAgeDays <= 0 || u.PasswordChanged.IsZero() {
		return false
	}
	return now.Sub(u.PasswordChanged) > time.Duration(p.MaxAgeDays)*24*time.Hour
}

// policyCache holds the policies for policyTTL.
type policyCache struct {
	mu      sync.Mutex
	fetched time.Time
	lock    types.UserLockPolicy
	expiry  types.PasswordExpirationPolicy
	basic   bool
}

// policies returns the lock and expiration policies, from the cache when
// it is fresh.
func (a *Authenticator) policies(
	ctx context.Context,
) (types.UserLockPolicy, types.PasswordExpirationPolicy, error) {
	c := &a.policy
	c.mu.Lock()
	defer c.mu.Unlock()
	now := a.now()
	if !c.fetched.IsZero() && now.Sub(c.fetched) < policyTTL {
		return c.lock, c.expiry, nil
	}
	lock, err := LockPolicy(ctx, a.settings)
	if err != nil {
		return lock, types.PasswordExpirationPolicy{}, err
	}
	expiry, err := ExpirationPolicy(ctx, a.settings)
	if err != nil {
		return lock, expiry, err
	}
	basic, err := BasicAuthEnabled(ctx, a.settings)
	if err != nil {
		return lock, expiry, err
	}
	c.lock, c.expiry, c.basic, c.fetched = lock, expiry, basic, now
	return lock, expiry, nil
}

// basicEnabled is the cached basic authentication switch.
func (a *Authenticator) basicEnabled(ctx context.Context) (bool, error) {
	if _, _, err := a.policies(ctx); err != nil {
		return false, err
	}
	a.policy.mu.Lock()
	defer a.policy.mu.Unlock()
	return a.policy.basic, nil
}

// ForgetPolicies drops the cached policies, so a change made through this
// instance applies to its next login.
func (a *Authenticator) ForgetPolicies() {
	a.policy.mu.Lock()
	a.policy.fetched = time.Time{}
	a.policy.mu.Unlock()
}

// recordFailure counts a wrong password against the lock policy. Best
// effort: failing to count must not turn a wrong password into a 500.
func (a *Authenticator) recordFailure(ctx context.Context, name string) {
	lock, _, err := a.policies(ctx)
	if err != nil || !lock.Enabled {
		return
	}
	_, _ = a.users.RecordFailedLogin(ctx, name, lock.LoginAttempts)
}

// accept finishes a login whose password verified: it applies the
// expiration policy, then records the login when the cache was filled (once
// per fill, not per request) or when failures need resetting.
func (a *Authenticator) accept(
	ctx context.Context,
	u types.User,
	now time.Time,
	filled bool,
) (Principal, error) {
	_, expiry, err := a.policies(ctx)
	if err != nil {
		return Principal{}, err
	}
	if PasswordExpired(u, expiry, now) {
		// The principal comes back with the error: the one request it may
		// make is changing its own password.
		p := principalOf(u)
		p.PasswordExpired = true
		return p, ErrPasswordExpired
	}
	if filled || u.FailedLogins > 0 {
		// Best effort: a failed write must not fail the login.
		_ = a.users.RecordLogin(ctx, u.Name, now)
	}
	return principalOf(u), nil
}
