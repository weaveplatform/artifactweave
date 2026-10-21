package auth

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Current rechecks an authenticated request after a potentially slow remote read.
// It reads only local state, so publication can call it inside its transaction.
// No password or token value is retained, forwarded or verified a second time.
func (a *Authenticator) Current(
	ctx context.Context,
	p Principal,
	tokens store.TokenStore,
) (Principal, error) {
	if p.Registry != nil {
		return Principal{}, ErrBadCredentials
	}
	if p.Anonymous {
		on, err := a.AnonymousAllowed(ctx)
		if err != nil {
			return Principal{}, err
		}
		if !on {
			return Principal{}, ErrBadCredentials
		}
		return Anonymous, nil
	}
	if p.TokenID != "" {
		t, err := tokens.Get(ctx, p.TokenID)
		if err != nil {
			return Principal{}, fmt.Errorf("auth: current token: %w", err)
		}
		if t.Expired(a.now()) {
			return Principal{}, ErrBadCredentials
		}
		u, err := a.tokenUser(ctx, t, p.Name)
		if err != nil {
			return Principal{}, err
		}
		if t.Purpose == types.TokenPurposeEncryptedPassword {
			if err = a.currentPassword(ctx, u); err != nil {
				return Principal{}, err
			}
		}
		fresh := tokenPrincipal(u, t.Scope)
		fresh.TokenID, fresh.TokenExpires, fresh.Identity = t.ID, t.ExpiresAt, t.Identity
		return fresh, nil
	}
	if !p.Password {
		return Principal{}, ErrBadCredentials
	}
	u, err := a.users.Get(ctx, p.Name)
	if err != nil {
		return Principal{}, fmt.Errorf("auth: current user: %w", err)
	}
	if u.Status != "" && u.Status != types.UserEnabled ||
		sha256.Sum256(u.PasswordHash) != p.passwordRevision {
		return Principal{}, ErrBadCredentials
	}
	on, err := BasicAuthEnabled(ctx, a.settings)
	if err != nil {
		return Principal{}, err
	}
	if !on {
		return Principal{}, ErrBadCredentials
	}
	if err = a.currentPassword(ctx, u); err != nil {
		return Principal{}, err
	}
	return principalOf(u), nil
}

// Publication needs current policy, not the login hot path's short policy cache.
func (a *Authenticator) currentPassword(ctx context.Context, u types.User) error {
	if len(u.PasswordHash) == 0 || u.InternalPasswordDisabled {
		return ErrBadCredentials
	}
	expiry, err := ExpirationPolicy(ctx, a.settings)
	if err != nil {
		return err
	}
	if PasswordExpired(u, expiry, a.now()) {
		return ErrPasswordExpired
	}
	return nil
}
