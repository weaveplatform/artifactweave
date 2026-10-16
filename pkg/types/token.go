package types

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidSubject reports a token subject that is not an Access subject.
var ErrInvalidSubject = errors.New("invalid token subject")

// Token defaults from the Access configuration (research 05).
const (
	DefaultTokenExpiry        = time.Hour
	DefaultRevocableThreshold = 6 * time.Hour
	DefaultRefreshGrace       = 24 * time.Hour
)

// TokenPurposeEncryptedPassword marks the reference token that stands in for
// a user's encrypted password (GET /api/security/encryptedPassword). A user
// has at most one; it keeps its value, sealed, so it can be shown again, and
// it is revoked whenever the password is set or expired.
//
//nolint:gosec // G101: a purpose name, not a credential
const TokenPurposeEncryptedPassword = "encrypted_password"

// TokenIdentity says how a token's subject resolves to a user. Access issues
// tokens from an OIDC token exchange without checking that the mapped user
// exists (research 21), so such a token may name a user who is not stored.
type TokenIdentity string

// Token identities.
const (
	// IdentityStored: the subject must be a stored, enabled user. Every token
	// but an OIDC exchange's.
	IdentityStored TokenIdentity = ""
	// IdentityTransient: a stored user of that name if there is one;
	// otherwise a transient user for the request, a member of the groups new
	// users join automatically.
	IdentityTransient TokenIdentity = "transient"
	// IdentitySynthetic: never a stored user and in no group but those the
	// scope names. An identity mapping without a user name issues it, so a
	// group-scoped token acts through exactly its groups.
	IdentitySynthetic TokenIdentity = "synthetic"
)

// Token is the stored record of an access token. The JWT itself is never
// stored: AccessDigest, RefreshDigest and ReferenceDigest are hashes used to
// revoke by value and to resolve refresh and reference tokens. A zero
// ExpiresAt means the token never expires.
type Token struct {
	ID              string
	Subject         string
	Scope           Scope
	Audience        string
	Description     string
	ProjectKey      string
	IssuedAt        time.Time
	ExpiresAt       time.Time
	Refreshable     bool
	Revocable       bool
	LastUsed        time.Time
	AccessDigest    []byte `json:"-"`
	RefreshDigest   []byte `json:"-"`
	ReferenceDigest []byte `json:"-"`
	// Purpose marks a token with a special role; empty for an ordinary one.
	// A subject has at most one token of each purpose.
	Purpose string
	// Secret is the reference token of a purposed token, kept so it can be
	// shown again; stores seal it. Empty for an ordinary token.
	Secret string `json:"-"`
	// Identity is how the subject resolves to a user.
	Identity TokenIdentity
	// Registry is set on a registry token alone, which the Docker token
	// endpoint issues and nothing stores (decision 0005): what it grants.
	Registry *RegistryAccess `json:"-"`
}

// Registry token defaults (decision 0005).
const (
	// RegistryTokenExpiry is a registry token's lifetime, shortened to the
	// remaining life of the credential that obtained it.
	RegistryTokenExpiry = time.Hour
)

// RegistryAccess is what a registry token grants: the scopes the client
// asked the token endpoint for, each with only the actions the caller held.
type RegistryAccess struct {
	// Realm is the repository key the token endpoint was addressed by, the
	// <repo> of .../api/docker/<repo>/v2/token or a subdomain's repository;
	// empty for null and for /v2/token on the repository path, where each
	// name begins with its repository key.
	Realm  string          `json:"realm,omitempty"`
	Grants []RegistryGrant `json:"access"`
}

// RegistryGrant is one granted scope in the Docker token specification's
// form: type repository with an image name as the client addresses it and
// the actions pull, push and delete, or type registry with name catalog.
type RegistryGrant struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// Expired reports whether the token has expired at now.
func (t Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

// Revocable applies Access's rule: a token is revocable when it never
// expires, lives at least the revocable threshold, or the caller forced it.
// Short-lived tokens are not tracked for revocation because they expire
// before a revocation list would matter.
func Revocable(expiresIn, threshold time.Duration, force bool) bool {
	return force || expiresIn == 0 || expiresIn >= threshold
}

// Subject is a parsed Access token subject, jfac@<service id>/users/<name>.
type Subject struct {
	ServiceID string
	Kind      string // "users" for user tokens
	Name      string
}

// String renders the subject.
func (s Subject) String() string { return s.ServiceID + "/" + s.Kind + "/" + s.Name }

// ParseSubject reads a token subject. The JFrog CLI extracts the user name
// from the "/users/" segment, so that form is the one artifactweave issues.
func ParseSubject(s string) (Subject, error) {
	service, rest, ok := strings.Cut(s, "/")
	if !ok || !strings.Contains(service, "@") {
		return Subject{}, fmt.Errorf("%w: %q", ErrInvalidSubject, s)
	}
	kind, name, ok := strings.Cut(rest, "/")
	if !ok || kind == "" || name == "" {
		return Subject{}, fmt.Errorf("%w: %q", ErrInvalidSubject, s)
	}
	return Subject{ServiceID: service, Kind: kind, Name: name}, nil
}

// referenceTokenPattern is the documented reference-token form: base64 of
// "reftkn:01..." ("cmVmd" plus 59 alphanumerics, 64 characters in all).
var referenceTokenPattern = regexp.MustCompile(`^cmVmd[A-Za-z0-9]{59}$`)

// IsReferenceToken reports whether s has the reference-token form, which is
// how the authenticator tells a reference token from a JWT or an API key.
func IsReferenceToken(s string) bool { return referenceTokenPattern.MatchString(s) }
