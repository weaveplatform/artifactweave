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

// ExpiredVerifier is a TokenVerifier that also verifies an access token
// whose only fault is its expiry. internal/auth/token implements it.
type ExpiredVerifier interface {
	VerifyExpired(ctx context.Context, raw string) (types.Token, error)
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
	u, err := a.tokenUser(ctx, t, user)
	if err != nil {
		return Principal{}, err
	}
	if t.Purpose == types.TokenPurposeEncryptedPassword {
		if err := a.passwordUsable(ctx, u); err != nil {
			return Principal{}, err
		}
	}
	p := tokenPrincipal(u, t.Scope)
	p.TokenID, p.TokenExpires, p.Identity = t.ID, t.ExpiresAt, t.Identity
	if t.Registry != nil {
		p.Registry, p.TokenParent = t.Registry, t.Parent
		if u.Name == AnonymousUser {
			// An anonymous registry token works only while anonymous
			// access does: turning it off ends the pulls at once.
			on, err := a.AnonymousAllowed(ctx)
			if err != nil {
				return Principal{}, err
			}
			if !on {
				return Principal{}, ErrBadCredentials
			}
			p.Anonymous = true
		}
	}
	return p, nil
}

// Refresher is the access token a refresh presents for the token it
// replaces, and that token's user.
type Refresher struct {
	User    string
	TokenID string
}

// ExpiredToken verifies raw as an access token whatever its expiry, and
// whose user is still enabled. Only the refresh grant asks: the JFrog CLI
// refreshes with the access token it is replacing, by then often expired,
// as the Bearer credential (Access) or the access_token form field
// (legacy).
func (a *Authenticator) ExpiredToken(ctx context.Context, raw string) (Refresher, bool) {
	v, ok := a.tokens.(ExpiredVerifier)
	if !ok {
		return Refresher{}, false
	}
	t, err := v.VerifyExpired(ctx, raw)
	if err != nil {
		return Refresher{}, false
	}
	u, err := a.tokenUser(ctx, t, "")
	if err != nil {
		return Refresher{}, false
	}
	return Refresher{User: u.Name, TokenID: t.ID}, true
}

// tokenUser returns the enabled user t's subject names, which must be user
// when user is set. A token from an OIDC exchange, or one an administrator
// minted for a name that is no stored user, may name a user who is not
// stored (types.TokenIdentity): a synthetic subject is never a stored user,
// and a transient one stands in for a missing user.
func (a *Authenticator) tokenUser(
	ctx context.Context,
	t types.Token,
	user string,
) (types.User, error) {
	sub, err := types.ParseSubject(t.Subject)
	if err != nil || sub.Kind != "users" || (user != "" && user != sub.Name) {
		return types.User{}, ErrBadCredentials
	}
	if t.Identity == types.IdentitySynthetic {
		return types.User{Name: sub.Name}, nil
	}
	u, err := a.users.Get(ctx, sub.Name)
	if errors.Is(err, store.ErrNotFound) && t.Identity == types.IdentityTransient {
		return a.transientUser(ctx, sub.Name)
	}
	if errors.Is(err, store.ErrNotFound) {
		return types.User{}, ErrBadCredentials
	}
	if err != nil {
		return types.User{}, fmt.Errorf("auth: user %s: %w", sub.Name, err)
	}
	if u.Status != "" && u.Status != types.UserEnabled {
		return types.User{}, ErrBadCredentials
	}
	return u, nil
}

// transientUser is a user a transient token names who is not stored. It
// exists for the request alone, and belongs to the groups new users join
// automatically, as Access treats such a user ("Transient Users and Auto
// Join Groups" in JFrog's access-tokens page; research 21). The token's
// scope then decides what applies: applied-permissions/user keeps those
// groups and grants to the name, a groups scope only the groups it names.
func (a *Authenticator) transientUser(ctx context.Context, name string) (types.User, error) {
	u := types.User{Name: name}
	if a.groups == nil {
		return u, nil
	}
	groups, err := a.groups.AutoJoin(ctx)
	if err != nil {
		return types.User{}, fmt.Errorf("auth: auto-join groups: %w", err)
	}
	u.Groups = groups
	return u, nil
}

// passwordUsable applies to an encrypted password what Basic applies to the
// password it stands for. Setting or expiring the password revokes it
// (internal/service/identity), but the age policy expires a password with
// no write to revoke on, so expiry is checked here as well.
func (a *Authenticator) passwordUsable(ctx context.Context, u types.User) error {
	if len(u.PasswordHash) == 0 || u.InternalPasswordDisabled {
		return ErrBadCredentials
	}
	_, expiry, err := a.policies(ctx)
	if err != nil {
		return err
	}
	if PasswordExpired(u, expiry, a.now()) {
		return ErrPasswordExpired
	}
	return nil
}

type expiredTokenKey struct{}

// WithExpiredToken returns ctx carrying the expired access token a refresh
// request presented as its credential (Authenticator.ExpiredToken).
func WithExpiredToken(ctx context.Context, r Refresher) context.Context {
	return context.WithValue(ctx, expiredTokenKey{}, r)
}

// ExpiredTokenFrom returns what WithExpiredToken stored.
func ExpiredTokenFrom(ctx context.Context) (Refresher, bool) {
	r, ok := ctx.Value(expiredTokenKey{}).(Refresher)
	return r, ok
}

// tokenPrincipal is the token's user with the token's scope. Authorize
// applies the scope (decision 0006): applied-permissions/user acts as the
// user, /admin is an administrator whatever its user, /groups
// acts through the named groups the user belongs to, and resource and
// system entries only narrow. The user's own flag and groups are kept here
// because a resource-only scope narrows them rather than replacing them.
func tokenPrincipal(u types.User, scope types.Scope) Principal {
	return Principal{
		Name: u.Name, Admin: u.Admin, Groups: slices.Clone(u.Groups), Scope: scope, token: true,
	}
}
