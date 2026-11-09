package auth

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// CurrentRegistry rechecks a verified registry credential without converting it
// into a general access token. Its original realm, grants, scope and expiry stay
// fixed; current user state and the parent credential can only remove access.
func (a *Authenticator) CurrentRegistry(
	ctx context.Context,
	p kernel.Principal,
	tokens port.TokenStore,
) (kernel.Principal, error) {
	if !p.IsTokenAuthenticated || p.Registry == nil || p.TokenID == "" || p.TokenExpires.IsZero() ||
		!a.now().Before(p.TokenExpires) {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	if p.TokenParent != "" {
		parent, err := tokens.Get(ctx, p.TokenParent)
		if err != nil {
			return kernel.Principal{}, fmt.Errorf("auth: registry parent: %w", err)
		}
		// Stored parent scopes are immutable. Refuse replacement of that record,
		// rather than allowing an old grant to inherit a changed scope/identity.
		if parent.Expired(a.now()) || parent.Subject != p.TokenSubject ||
			parent.Identity != p.Identity || parent.Scope.String() != p.Scope.String() {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
	}
	u, err := a.tokenUser(ctx, model.Token{Subject: p.TokenSubject, Identity: p.Identity}, p.Name)
	if err != nil {
		return kernel.Principal{}, err
	}
	if p.Anonymous {
		on, err := a.AnonymousAllowed(ctx)
		if err != nil {
			return kernel.Principal{}, err
		}
		if !on {
			return kernel.Principal{}, kernel.ErrBadCredentials
		}
	} else if p.Identity == "" && sha256.Sum256(u.PasswordHash) != p.PasswordRevision {
		return kernel.Principal{}, kernel.ErrBadCredentials
	}
	fresh := tokenPrincipal(u, p.Scope)
	fresh.TokenID, fresh.TokenExpires, fresh.Identity = p.TokenID, p.TokenExpires, p.Identity
	fresh.Registry, fresh.TokenParent, fresh.Anonymous = p.Registry, p.TokenParent, p.Anonymous
	fresh.TokenSubject, fresh.PasswordRevision = p.TokenSubject, p.PasswordRevision
	return fresh, nil
}
