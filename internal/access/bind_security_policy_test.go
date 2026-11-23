package access_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// The security policy binding reads the defaults, checks with the REST
// APIs' rules, writes in the caller's transaction and unlocks when locking
// is turned off, through API and the store-only constructor alike.
func TestUnitSecurityPolicyBinding(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	defaults := accessapi.SecurityPolicy{
		LoginAttempts: authn.DefaultLoginAttempts, PasswordMaxAge: authn.DefaultPasswordMaxAgeDays, NotifyByEmail: true,
		Encryption: accessapi.EncryptionSupported,
	}
	for name, b := range map[string]accessapi.SecurityPolicies{
		"API": w.api.Security, "SecurityPoliciesOf": access.SecurityPoliciesOf(w.st),
	} {
		if got, err := b.Current(ctx); err != nil || got != defaults {
			t.Fatalf("%s Current = %+v, %v", name, got, err)
		}
		want := defaults
		want.LockEnabled, want.LoginAttempts, want.Encryption = true, 2, accessapi.EncryptionRequired
		want.HideUnauthorizedResources = true
		err := w.st.Tx(ctx, func(ctx context.Context) error {
			if err := b.Set(ctx, want); err != nil {
				return err
			}
			if got, err := b.Current(ctx); err != nil || got != want {
				t.Fatalf("%s read back %+v, %v", name, got, err)
			}
			if hide, err := b.HidesUnauthorized(ctx); err != nil || !hide {
				t.Fatalf("%s HidesUnauthorized = %v, %v", name, hide, err)
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatal(err)
		}
		if got, _ := b.Current(ctx); got != defaults {
			t.Fatalf("%s: a rolled-back Set left %+v", name, got)
		}
		if hide, err := b.HidesUnauthorized(ctx); err != nil || hide {
			t.Fatalf("%s: a rolled-back Set left hiding %v, %v", name, hide, err)
		}
		b.Forget()
	}
	// No loginAttempts is the default, as through REST.
	if got, err := w.api.Security.Check(accessapi.SecurityPolicy{PasswordMaxAge: 1, Encryption: "required"}); err != nil ||
		got.LoginAttempts != authn.DefaultLoginAttempts {
		t.Fatal(got, err)
	}
	for p, want := range map[accessapi.SecurityPolicy]string{
		{LoginAttempts: -1, PasswordMaxAge: 1, Encryption: "supported"}: "security.userLockPolicy: loginAttempts must be at least 1",
		{LoginAttempts: 1, Encryption: "supported"}:                     "security.passwordSettings.expirationPolicy: passwordMaxAge must be at least 1 day",
		{LoginAttempts: 1, PasswordMaxAge: 1, Encryption: "sometimes"}:  "security.passwordSettings: encryptionPolicy must be REQUIRED, SUPPORTED or UNSUPPORTED",
	} {
		_, err := w.api.Security.Check(p)
		var refused *accessapi.SecurityPolicyInvalid
		if !errors.As(err, &refused) || !errors.Is(err, kernel.ErrInvalid) || err.Error() != want {
			t.Errorf("%+v: %v", p, err)
		}
		if err := w.api.Security.Set(ctx, p); !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("Set %+v: %v", p, err)
		}
	}
	// Turning locking off unlocks everyone.
	locked := w.ann
	locked.Status = model.UserLocked
	must(t, w.st.Users().Update(ctx, locked))
	must(t, w.api.Security.Set(ctx, defaults))
	if names, _ := w.st.Users().Locked(ctx); len(names) != 0 {
		t.Fatal("still locked", names)
	}
}

// Forget makes this instance's next login apply a committed change at once.
func TestUnitSecurityPolicyForget(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	if _, err := w.api.Authenticator.Basic(ctx, "ann", "secret"); err != nil {
		t.Fatal(err)
	}
	must(t, w.st.Settings().Put(ctx, settings.SettingPasswordEncryption, accessapi.EncryptionRequired))
	if _, err := w.api.Authenticator.Basic(ctx, "ann", "secret"); err != nil {
		t.Fatal("the cached policy should still apply", err)
	}
	w.api.Security.Forget()
	if _, err := w.api.Authenticator.Basic(ctx, "ann", "secret"); !errors.Is(err, authn.ErrClearTextPassword) {
		t.Fatal(err)
	}
}

// The binding returns each store failure as the store gave it: the read of
// each policy and each write.
func TestUnitSecurityPolicyStoreFailures(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	boom := errors.New("settings down")
	for _, key := range []string{
		settings.SettingUserLockPolicy, settings.SettingPasswordExpirationPolicy, settings.SettingPasswordEncryption,
		settings.SettingHideUnauthorizedResources,
	} {
		b := access.SecurityPoliciesOf(keyedFailure{Store: w.st, key: key, err: boom})
		if _, err := b.Current(ctx); !errors.Is(err, boom) {
			t.Errorf("Current with %s failing: %v", key, err)
		}
		if err := b.Set(ctx, accessapi.SecurityPolicy{
			LockEnabled: true, LoginAttempts: 1, PasswordMaxAge: 1, Encryption: accessapi.EncryptionSupported,
		}); !errors.Is(err, boom) {
			t.Errorf("Set with %s failing: %v", key, err)
		}
		if _, err := b.HidesUnauthorized(ctx); key == settings.SettingHideUnauthorizedResources && !errors.Is(err, boom) {
			t.Errorf("HidesUnauthorized with %s failing: %v", key, err)
		}
	}
}

// keyedFailure fails reading and writing one setting.
type keyedFailure struct {
	access.Store
	key string
	err error
}

func (s keyedFailure) Settings() settings.SettingsStore {
	return keyedSettings{SettingsStore: s.Store.Settings(), key: s.key, err: s.err}
}

type keyedSettings struct {
	settings.SettingsStore
	key string
	err error
}

func (s keyedSettings) Get(ctx context.Context, key string, into any) error {
	if key == s.key {
		return s.err
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // a pass-through test double
}

func (s keyedSettings) Put(ctx context.Context, key string, v any) error {
	if key == s.key {
		return s.err
	}
	return s.SettingsStore.Put(ctx, key, v) //nolint:wrapcheck // a pass-through test double
}
