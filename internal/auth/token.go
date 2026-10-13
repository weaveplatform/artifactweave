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
	return tokenPrincipal(u, t.Scope), nil
}

// tokenPrincipal applies the parts of a scope that decide who the request
// is: applied-permissions/user acts as the user (an administrator stays
// one), /admin is administrator only for a user who still is one, and
// /groups acts as members of the named groups only. Resource and system
// entries narrow what that principal may do; the permission evaluator
// applies them, from Principal.Scope.
func tokenPrincipal(u types.User, scope types.Scope) Principal {
	p := Principal{Name: u.Name, Scope: scope}
	for _, e := range scope.Entries {
		switch e.Kind {
		case types.ScopeAdmin:
			p.Admin = p.Admin || u.Admin
		case types.ScopeUser:
			p.Admin = p.Admin || u.Admin
			p.Groups = append(p.Groups, u.Groups...)
		case types.ScopeGroups:
			p.Groups = append(p.Groups, e.Names...)
		default:
		}
	}
	slices.Sort(p.Groups)
	p.Groups = slices.Compact(p.Groups)
	return p
}
