package access

import (
	"context"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	identitycore "github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

var _ accessapi.SecurityPolicies = securityPolicies{}

// securityPolicies binds the descriptor's baseline security policy to the
// settings the Artifactory and Access REST APIs write, checked by the same
// identity rules, so every surface accepts, stores and enforces one policy.
type securityPolicies struct {
	st Store
	// a is this instance's authenticator, whose policy cache Forget drops;
	// nil when the binding was made before any authenticator, as config
	// apply and the bootstrap descriptor make it.
	a *authn.Authenticator
}

func (b securityPolicies) Current(ctx context.Context) (accessapi.SecurityPolicy, error) {
	store := b.st.Settings()
	lock, err := authn.LockPolicy(ctx, store)
	if err != nil {
		return accessapi.SecurityPolicy{}, err //nolint:wrapcheck // already wrapped
	}
	expiry, err := authn.ExpirationPolicy(ctx, store)
	if err != nil {
		return accessapi.SecurityPolicy{}, err //nolint:wrapcheck // already wrapped
	}
	encryption, err := authn.PasswordEncryption(ctx, store)
	if err != nil {
		return accessapi.SecurityPolicy{}, err //nolint:wrapcheck // already wrapped
	}
	return accessapi.SecurityPolicy{
		LockEnabled: lock.Enabled, LoginAttempts: lock.LoginAttempts,
		ExpirationEnabled: expiry.Enabled, PasswordMaxAge: expiry.MaxAgeDays, NotifyByEmail: expiry.NotifyByEmail,
		Encryption: encryption,
	}, nil
}

func (b securityPolicies) Check(p accessapi.SecurityPolicy) (accessapi.SecurityPolicy, error) {
	lock, err := identitycore.CheckLockPolicy(model.UserLockPolicy{Enabled: p.LockEnabled, LoginAttempts: p.LoginAttempts})
	if err != nil {
		return p, refusal("userLockPolicy", err)
	}
	p.LoginAttempts = lock.LoginAttempts
	if err := identitycore.CheckExpirationPolicy(expiration(p)); err != nil {
		return p, refusal("passwordSettings.expirationPolicy", err)
	}
	if err := identitycore.CheckPasswordEncryption(p.Encryption); err != nil {
		return p, refusal("passwordSettings", err)
	}
	return p, nil
}

func (b securityPolicies) Set(ctx context.Context, p accessapi.SecurityPolicy) error {
	p, err := b.Check(p)
	if err != nil {
		return err
	}
	//nolint:wrapcheck // the store's errors are the contract's
	return b.st.Tx(ctx, func(ctx context.Context) error {
		if err := b.st.Projects().Guard(ctx); err != nil {
			return err //nolint:wrapcheck // preserve the shared guard failure
		}
		store := b.st.Settings()
		for _, w := range []struct {
			key string
			v   any
		}{
			{settings.SettingUserLockPolicy, model.UserLockPolicy{Enabled: p.LockEnabled, LoginAttempts: p.LoginAttempts}},
			{settings.SettingPasswordExpirationPolicy, expiration(p)},
			{settings.SettingPasswordEncryption, p.Encryption},
		} {
			if err := store.Put(ctx, w.key, w.v); err != nil {
				return err //nolint:wrapcheck // a store error
			}
		}
		if !p.LockEnabled {
			// As identity.SetLockPolicy: turning the policy off never
			// strands a locked user.
			return b.st.Users().Unlock(ctx) //nolint:wrapcheck // a store error
		}
		return nil
	})
}

func (b securityPolicies) Forget() {
	if b.a != nil {
		b.a.ForgetPolicies()
	}
}

func expiration(p accessapi.SecurityPolicy) model.PasswordExpirationPolicy {
	return model.PasswordExpirationPolicy{
		Enabled: p.ExpirationEnabled, MaxAgeDays: p.PasswordMaxAge, NotifyByEmail: p.NotifyByEmail,
	}
}

// refusal turns identity's refusal into the contract's, naming the
// descriptor's section beside the field the rule names. The Check functions
// refuse only with identity's ErrInvalid.
func refusal(section string, err error) error {
	reason := strings.TrimPrefix(err.Error(), identitycore.ErrInvalid.Error()+": ")
	return &accessapi.SecurityPolicyInvalid{Section: section, Reason: reason}
}
