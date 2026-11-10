package authn

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Current rechecks an authenticated request after a potentially slow remote read.
// It reads only local state, so publication can call it inside its transaction.
// No password or token value is retained, forwarded or verified a second time.
func (a *Authenticator) Current(
	ctx context.Context,
	p kernel.Principal,
	tokens port.TokenStore,
) (kernel.Principal, error) {
	if p.Registry != nil {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	if p.Anonymous {
		on, err := a.AnonymousAllowed(ctx)
		if err != nil {
			return kernel.Principal{}, err
		}
		if !on {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
		return kernel.Anonymous, nil
	}
	if p.TokenID != "" {
		t, err := tokens.Get(ctx, p.TokenID)
		if err != nil {
			return kernel.Principal{}, fmt.Errorf("auth: current token: %w", err)
		}
		if t.Expired(a.now()) {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
		u, err := a.tokenUser(ctx, t, p.Name)
		if err != nil {
			return kernel.Principal{}, err
		}
		if t.Purpose == model.TokenPurposeEncryptedPassword {
			if err = a.currentPassword(ctx, u); err != nil {
				return kernel.Principal{}, err
			}
		}
		fresh := tokenPrincipal(u, t.Scope)
		fresh.TokenID, fresh.TokenExpires, fresh.Identity = t.ID, t.ExpiresAt, t.Identity
		return fresh, nil
	}
	if !p.Password {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	u, err := a.users.Get(ctx, p.Name)
	if err != nil {
		return kernel.Principal{}, fmt.Errorf("auth: current user: %w", err)
	}
	if u.Status != "" && u.Status != model.UserEnabled ||
		sha256.Sum256(u.PasswordHash) != p.PasswordRevision {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	on, err := BasicAuthEnabled(ctx, a.settings)
	if err != nil {
		return kernel.Principal{}, err
	}
	if !on {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	if err = a.currentPassword(ctx, u); err != nil {
		return kernel.Principal{}, err
	}
	if err := a.apiAllowed(ctx, u.Name); err != nil {
		return kernel.Principal{}, err
	}
	return principalOf(u), nil
}

// Publication needs current policy, not the login hot path's short policy cache.
func (a *Authenticator) currentPassword(ctx context.Context, u model.User) error {
	if len(u.PasswordHash) == 0 || u.InternalPasswordDisabled {
		return kernel.ErrBadCredentials
	}
	expiry, err := ExpirationPolicy(ctx, a.settings)
	if err != nil {
		return err
	}
	if PasswordExpired(u, expiry, a.now()) {
		return kernel.ErrPasswordExpired
	}
	return nil
}
