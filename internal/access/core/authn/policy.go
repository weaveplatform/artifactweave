package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	platformsettings "github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// The policies' defaults when nothing is stored, as Artifactory ships them.
const (
	// DefaultLoginAttempts is how many failed logins lock an account while
	// the user lock policy is on.
	DefaultLoginAttempts = 5
	// DefaultPasswordMaxAgeDays is how many days an internal password lasts
	// while the password expiration policy is on.
	DefaultPasswordMaxAgeDays = 60
)

// policyTTL is how long the authenticator reuses the stored policies. Every
// Basic login needs them, and a settings read per request would put the
// database on the hot path; a policy changed on another instance takes
// effect here within this long.
const policyTTL = 10 * time.Second

// ErrBasicAuthDisabled answers an internal password while internal password
// login is off (basic_authentication_enabled is false) and no directory
// accepted the password either.
var ErrBasicAuthDisabled = apierror.New(http.StatusUnauthorized,
	"Basic authentication with a password is disabled; use an access token")

// ErrClearTextPassword answers a clear-text password outside a UI request
// while the password encryption policy is required. JFrog documents only
// that "clear-text authentication fails"; the wording is artifactweave's.
var ErrClearTextPassword = apierror.New(http.StatusUnauthorized,
	"Only encrypted passwords are accepted; send the encrypted password instead of the clear-text password")

// BasicAuthEnabled reports whether internal passwords log in over HTTP
// Basic: Access's security.authentication.basic-authentication-enabled,
// whose UI checkbox is "Disable Internal Password Login". It governs
// internal passwords and the encrypted passwords standing for them, never a
// directory's password. Unset means on, as Artifactory ships; enabling the
// first LDAP connection writes it off (access/core/ldap).
func BasicAuthEnabled(ctx context.Context, settings platformsettings.SettingsStore) (bool, error) {
	on := true
	if err := settings.Get(ctx, platformsettings.SettingBasicAuthEnabled, &on); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return on, fmt.Errorf("auth: basic authentication switch: %w", err)
	}
	return on, nil
}

// PasswordEncryption returns the stored password encryption policy, or
// supported, the default.
func PasswordEncryption(
	ctx context.Context,
	settings platformsettings.SettingsStore,
) (accessapi.PasswordEncryption, error) {
	p := accessapi.EncryptionSupported
	if err := settings.Get(ctx, platformsettings.SettingPasswordEncryption, &p); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return accessapi.EncryptionSupported, fmt.Errorf("auth: password encryption policy: %w", err)
	}
	return p, nil
}

type uiRequestKey struct{}

// WithUIRequest marks ctx as a request 7.x counts as its UI's
// (UiRequestUtils.isUiRestRequest), which the required encryption policy
// lets send a clear-text password: the UI is where 7.x's users obtain the
// encrypted password a remote client must then send. Without the exception
// no user could ever obtain one, since GET /api/security/encryptedPassword
// itself needs the clear-text password.
func WithUIRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, uiRequestKey{}, true)
}

func uiRequest(ctx context.Context) bool {
	on, _ := ctx.Value(uiRequestKey{}).(bool)
	return on
}

// LockPolicy returns the stored user lock policy, or the default.
func LockPolicy(
	ctx context.Context,
	settings platformsettings.SettingsStore,
) (model.UserLockPolicy, error) {
	p := model.UserLockPolicy{LoginAttempts: DefaultLoginAttempts}
	if err := settings.Get(ctx, platformsettings.SettingUserLockPolicy, &p); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return p, fmt.Errorf("auth: user lock policy: %w", err)
	}
	return p, nil
}

// AccessLockSettings returns the stored Access user lock settings, or
// model.DefaultAccessLockSettings.
func AccessLockSettings(
	ctx context.Context,
	settings platformsettings.SettingsStore,
) (model.AccessLockSettings, error) {
	p := model.DefaultAccessLockSettings()
	if err := settings.Get(ctx, platformsettings.SettingAccessLockSettings, &p); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return p, fmt.Errorf("auth: access lock settings: %w", err)
	}
	return p, nil
}

// ExpirationPolicy returns the stored password expiration policy, or the
// default.
func ExpirationPolicy(
	ctx context.Context,
	settings platformsettings.SettingsStore,
) (model.PasswordExpirationPolicy, error) {
	p := model.PasswordExpirationPolicy{MaxAgeDays: DefaultPasswordMaxAgeDays, NotifyByEmail: true}
	if err := settings.Get(ctx, platformsettings.SettingPasswordExpirationPolicy, &p); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return p, fmt.Errorf("auth: password expiration policy: %w", err)
	}
	return p, nil
}

// PasswordExpired reports whether u must set a new password. An
// administrator's expiry applies whatever the policy; age counts only from
// a recorded change, so users whose password predates the record (the
// seeded admin of an older install) are not locked out by enabling it.
func PasswordExpired(u model.User, p model.PasswordExpirationPolicy, now time.Time) bool {
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
	mu             sync.Mutex
	fetched        time.Time
	lock           model.UserLockPolicy
	expiry         model.PasswordExpirationPolicy
	allowBasicAuth bool
	access         model.AccessLockSettings
	encryption     accessapi.PasswordEncryption
}

// policies returns the lock and expiration policies, from the cache when
// it is fresh.
func (a *Authenticator) policies(
	ctx context.Context,
) (model.UserLockPolicy, model.PasswordExpirationPolicy, error) {
	c := &a.policy
	c.mu.Lock()
	defer c.mu.Unlock()
	now := a.now()
	if !c.fetched.IsZero() && now.Sub(c.fetched) < policyTTL {
		return c.lock, c.expiry, nil
	}
	lock, err := LockPolicy(ctx, a.settings)
	if err != nil {
		return lock, model.PasswordExpirationPolicy{}, err
	}
	expiry, err := ExpirationPolicy(ctx, a.settings)
	if err != nil {
		return lock, expiry, err
	}
	basic, err := BasicAuthEnabled(ctx, a.settings)
	if err != nil {
		return lock, expiry, err
	}
	access, err := AccessLockSettings(ctx, a.settings)
	if err != nil {
		return lock, expiry, err
	}
	encryption, err := PasswordEncryption(ctx, a.settings)
	if err != nil {
		return lock, expiry, err
	}
	c.lock, c.expiry, c.allowBasicAuth, c.access, c.encryption = lock, expiry, basic, access, encryption
	c.fetched = now
	return lock, expiry, nil
}

// encryption is the cached password encryption policy.
func (a *Authenticator) encryption(ctx context.Context) (accessapi.PasswordEncryption, error) {
	if _, _, err := a.policies(ctx); err != nil {
		return accessapi.EncryptionSupported, err
	}
	a.policy.mu.Lock()
	defer a.policy.mu.Unlock()
	return a.policy.encryption, nil
}

// basicEnabled is the cached basic authentication switch.
func (a *Authenticator) basicEnabled(ctx context.Context) (bool, error) {
	if _, _, err := a.policies(ctx); err != nil {
		return false, err
	}
	a.policy.mu.Lock()
	defer a.policy.mu.Unlock()
	return a.policy.allowBasicAuth, nil
}

// suspension is the cached temporary login suspension settings; ok is false
// when they cannot be read, and then nothing is suspended.
func (a *Authenticator) suspension(ctx context.Context) (model.AccessLockSettings, bool) {
	if _, _, err := a.policies(ctx); err != nil {
		return model.AccessLockSettings{}, false
	}
	a.policy.mu.Lock()
	defer a.policy.mu.Unlock()
	return a.policy.access, true
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
	if a.failures != nil {
		_ = a.failures.RecordFailedLogin(ctx, name, lock.LoginAttempts)
		return
	}
	_, _ = a.users.RecordFailedLogin(ctx, name, lock.LoginAttempts)
}

// accept finishes a login whose password verified: it applies the
// expiration policy, then records the login when the cache was filled (once
// per fill, not per request) or when failures need resetting.
func (a *Authenticator) accept(
	ctx context.Context,
	u model.User,
	now time.Time,
	filled bool,
) (kernel.Principal, error) {
	if err := a.apiAllowed(ctx, u.Name); err != nil {
		return kernel.Principal{}, err
	}
	_, expiry, err := a.policies(ctx)
	if err != nil {
		return kernel.Principal{}, err
	}
	if PasswordExpired(u, expiry, now) {
		// The principal comes back with the error: the one request it may
		// make is changing its own password.
		p := principalOf(u)
		p.PasswordExpired = true
		return p, kernel.ErrPasswordExpired
	}
	if filled || u.FailedLogins > 0 {
		// Best effort: a failed write must not fail the login.
		_ = a.users.RecordLogin(ctx, u.Name, now)
	}
	return principalOf(u), nil
}
