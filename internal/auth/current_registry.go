package auth

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// CurrentRegistry rechecks a verified registry credential without converting it
// into a general access token. Its original realm, grants, scope and expiry stay
// fixed; current user state and the parent credential can only remove access.
func (a *Authenticator) CurrentRegistry(
	ctx context.Context,
	p Principal,
	tokens store.TokenStore,
) (Principal, error) {
	if !p.isTokenAuthenticated || p.Registry == nil || p.TokenID == "" || p.TokenExpires.IsZero() ||
		!a.now().Before(p.TokenExpires) {
		return Principal{}, ErrBadCredentials
	}
	if p.TokenParent != "" {
		parent, err := tokens.Get(ctx, p.TokenParent)
		if err != nil {
			return Principal{}, fmt.Errorf("auth: registry parent: %w", err)
		}
		// Stored parent scopes are immutable. Refuse replacement of that record,
		// rather than allowing an old grant to inherit a changed scope/identity.
		if parent.Expired(a.now()) || parent.Subject != p.tokenSubject ||
			parent.Identity != p.Identity || parent.Scope.String() != p.Scope.String() {
			return Principal{}, ErrBadCredentials
		}
	}
	u, err := a.tokenUser(ctx, types.Token{Subject: p.tokenSubject, Identity: p.Identity}, p.Name)
	if err != nil {
		return Principal{}, err
	}
	if p.Anonymous {
		on, err := a.AnonymousAllowed(ctx)
		if err != nil {
			return Principal{}, err
		}
		if !on {
			return Principal{}, ErrBadCredentials
		}
	} else if p.Identity == "" && sha256.Sum256(u.PasswordHash) != p.passwordRevision {
		return Principal{}, ErrBadCredentials
	}
	fresh := tokenPrincipal(u, p.Scope)
	fresh.TokenID, fresh.TokenExpires, fresh.Identity = p.TokenID, p.TokenExpires, p.Identity
	fresh.Registry, fresh.TokenParent, fresh.Anonymous = p.Registry, p.TokenParent, p.Anonymous
	fresh.tokenSubject, fresh.passwordRevision = p.tokenSubject, p.passwordRevision
	return fresh, nil
}
