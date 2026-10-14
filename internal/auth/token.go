package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrInvalidToken reports a token that does not verify: malformed, badly
// signed, expired, revoked, or of a subject that is not a user. Verifiers
// wrap it; the request answers ErrBadCredentials.
var ErrInvalidToken = errors.New("invalid token")

// TokenVerifier resolves an access token (a JWT) or a reference token to its
// stored record. internal/auth/token implements it.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (types.Token, error)
}

// LooksLikeToken reports whether a Basic password is an access or reference
// token rather than a password: the JFrog CLI sends tokens as the password
// of a Basic credential.
func LooksLikeToken(s string) bool {
	return types.IsReferenceToken(s) ||
		(strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2)
}

// Token verifies a token presented as Bearer, as X-JFrog-Art-Api, or as the
// password of a Basic credential whose user name is user ("" for the first
// two). A Basic user name must name the token's subject (decision 0005). The
// subject must still be an enabled user: disabling a user disables their
// tokens.
func (a *Authenticator) Token(ctx context.Context, raw, user string) (Principal, error) {
	if a.tokens == nil {
		return Principal{}, ErrBadCredentials
	}
	t, err := a.tokens.Verify(ctx, raw)
	if errors.Is(err, ErrInvalidToken) {
		return Principal{}, ErrBadCredentials
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: token: %w", err)
	}
	sub, err := types.ParseSubject(t.Subject)
	if err != nil || sub.Kind != "users" || (user != "" && user != sub.Name) {
		return Principal{}, ErrBadCredentials
	}
	u, err := a.users.Get(ctx, sub.Name)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, ErrBadCredentials
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: user %s: %w", sub.Name, err)
	}
	if u.Status != "" && u.Status != types.UserEnabled {
		return Principal{}, ErrBadCredentials
	}
	p := tokenPrincipal(u, t.Scope)
	p.TokenID = t.ID
	return p, nil
}

// tokenPrincipal is the token's user with the token's scope. Authorize
// applies the scope (decision 0006): applied-permissions/user acts as the
// user, /admin is administrator only for a user who still is one, /groups
// acts through the named groups the user belongs to, and resource and
// system entries only narrow. The user's own flag and groups are kept here
// because a resource-only scope narrows them rather than replacing them.
func tokenPrincipal(u types.User, scope types.Scope) Principal {
	return Principal{
		Name: u.Name, Admin: u.Admin, Groups: slices.Clone(u.Groups), Scope: scope, token: true,
	}
}
