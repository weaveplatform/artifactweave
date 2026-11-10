package api

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrNotRevocable refuses to revoke a token shorter than the revocable
// threshold that was neither forced revocable nor refreshable.
var ErrNotRevocable = errors.New("token: token is not revocable")

// The token lifetimes that other modules apply to the tokens access issues
// (decision 0005).
const (
	// DefaultRefreshGrace is how long after a token expires its refresh
	// token is still accepted, so its record is kept that long.
	DefaultRefreshGrace = 24 * time.Hour
	// RegistryTokenExpiry is a registry token's lifetime, shortened to the
	// remaining life of the credential that obtained it.
	RegistryTokenExpiry = time.Hour
)

// referenceTokenPattern is the documented reference-token form: base64 of
// "reftkn:01..." ("cmVmd" plus 59 alphanumerics, 64 characters in all).
var referenceTokenPattern = regexp.MustCompile(`^cmVmd[A-Za-z0-9]{59}$`)

// IsReferenceToken reports whether s has the reference-token form, which is
// how the authenticator tells a reference token from a JWT or an API key.
func IsReferenceToken(s string) bool { return referenceTokenPattern.MatchString(s) }

// LooksLikeToken reports whether a Basic password is an access or reference
// token rather than a password: the JFrog CLI sends tokens as the password
// of a Basic credential.
func LooksLikeToken(s string) bool {
	return IsReferenceToken(s) ||
		(strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2)
}

// TokenRequest asks Tokens.Issue for an access token for a user. It has every
// field of the token service's own request, in the same order and of the same
// types, so the access module converts one to the other and issuance keeps
// every rule of decision 0005.
type TokenRequest struct {
	// Username is the user the token is for: required, and without a slash.
	Username string
	// Scope defaults to applied-permissions/user.
	Scope kernel.Scope
	// ExpiresIn defaults to token.default_expiry, one year while no
	// administrator has set it; zero means never and needs ByAdmin.
	ExpiresIn      *time.Duration
	Refreshable    bool
	ForceRevocable bool
	Audience       string
	Description    string
	ProjectKey     string
	// IncludeReference also issues a reference token.
	IncludeReference bool
	// ByAdmin is whether the caller is an administrator.
	ByAdmin bool
	// Purpose marks a token its user holds at most one of, such as the
	// encrypted password's ("encrypted_password"). Such a token always has a
	// reference token, and keeps it sealed so it can be shown again.
	Purpose string
	// Identity is how the token's subject resolves to a user. An OIDC token
	// exchange, and an administrator minting for a name that is no stored
	// user, issue a token for a user who need not be stored.
	Identity kernel.TokenIdentity
	// NotAfter, when set, is the latest the token may expire; a longer or
	// unending lifetime is cut to it. A transient user's token for itself
	// sets it to the expiry of the token that minted it, so the token never
	// outlives the 24 hours an OIDC mapping allows.
	NotAfter time.Time
}

// RegistryTokenRequest asks Tokens.IssueRegistry for a registry token, which
// the Docker token endpoint hands out (decision 0005). It has every field of
// the token service's registry request, in the same order and of the same
// types.
type RegistryTokenRequest struct {
	// Username is the user the token acts for: required, and without a
	// slash.
	Username string
	// ExpiresIn is the lifetime; it must be positive and at most one hour,
	// and the caller shortens it to the remaining life of the credential
	// that obtained the token.
	ExpiresIn time.Duration
	// Access is what the token grants, in the Docker token specification's
	// form.
	Access kernel.RegistryAccess
	// Scope is the scope of the credential that obtained the token, which
	// the token keeps so it never acts more broadly; it defaults to
	// applied-permissions/user, a password's.
	Scope kernel.Scope
	// Identity is that credential's identity when it came from an OIDC
	// exchange, so the token resolves to the same transient or synthetic
	// user it does.
	Identity kernel.TokenIdentity
	// Parent is the id of the stored token that obtained it, empty for a
	// password or anonymous caller. The token is refused once the parent is
	// revoked.
	Parent string
}

// IssuedToken is a new token and its secrets, which are never stored and so
// cannot be shown again. RefreshToken is set only for a refreshable token,
// and ReferenceToken only when the request asked for one or had a purpose.
type IssuedToken struct {
	AccessToken, RefreshToken, ReferenceToken string
	// ExpiresIn is the lifetime in seconds; zero means never.
	ExpiresIn int64
	// TokenID is the token's id, which its record and its jti claim carry.
	TokenID string
	// IssuedAt is when the token was issued, in whole seconds, as its iat
	// claim carries it.
	IssuedAt time.Time
}

// Tokens issues and revokes access tokens (decision 0005). Each call uses ctx
// for the stores it reads and writes, so a call inside a store transaction
// takes part in it. The errors are the token service's: callers test them
// with errors.Is.
type Tokens interface {
	// Issue creates and stores a token. It refuses a request without a
	// user name, with a negative lifetime, for a token that never expires
	// from a caller who is not an administrator, or whose NotAfter has
	// passed, and a user whose API access is restricted; a second token of a
	// purpose the user already holds is an error matching
	// kernel.ErrConflict.
	Issue(ctx context.Context, r TokenRequest) (IssuedToken, error)
	// IssueRegistry signs a registry token. Nothing stores it, as
	// Artifactory stores no token below its persistence threshold, so it cannot be
	// listed or revoked; its short life, its grants and the user's current
	// permissions bound it instead.
	IssueRegistry(ctx context.Context, r RegistryTokenRequest) (IssuedToken, error)
	// RevokeValue revokes the stored token whose access or reference token
	// is raw: an error matching kernel.ErrNotFound when no stored token has
	// that value, and ErrNotRevocable when the token is short-lived and was
	// neither forced revocable nor refreshable.
	RevokeValue(ctx context.Context, raw string) error
	// EncryptedPassword returns user's encrypted password: the reference
	// token of their encrypted-password token, which never expires. It is
	// created on first use and the same value after, so every client given
	// it keeps working until the password changes and revokes it.
	EncryptedPassword(ctx context.Context, user string) (string, error)
	// DeleteExpired removes the records of tokens that expired before
	// before and returns how many it removed; tokens that never expire stay.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}
