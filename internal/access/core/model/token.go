package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrInvalidSubject reports a token subject that is not an Access subject.
var ErrInvalidSubject = errors.New("invalid token subject")

// Token defaults from the Access configuration (research 05).
const (
	// DefaultTokenExpiry is token.default-expiry, the lifetime of an Access
	// token created without expires_in while no administrator has set one:
	// one year, as the Access configuration reference and the JFrog CLI give
	// it. JFrog's access-tokens page says 3600 s instead; decision 0005
	// records the conflict, which the live check on #216 settles.
	DefaultTokenExpiry        = 365 * 24 * time.Hour
	DefaultRevocableThreshold = 6 * time.Hour
	// LegacyTokenExpiry is the lifetime of a token the legacy
	// POST /api/security/token creates without expires_in. It is that
	// route's own default, not token.default-expiry, so raising the Access
	// default leaves the legacy route as it was (research 05 lists the 7.x
	// value as unconfirmed).
	LegacyTokenExpiry = time.Hour
)

// TokenPurposeEncryptedPassword marks the reference token that stands in for
// a user's encrypted password (GET /api/security/encryptedPassword). A user
// has at most one; it keeps its value, sealed, so it can be shown again, and
// it is revoked whenever the password is set or expired.
//
//nolint:gosec // G101: a purpose name, not a credential
const TokenPurposeEncryptedPassword = "encrypted_password"

// Token is the stored record of an access token. The JWT itself is never
// stored: AccessDigest, RefreshDigest and ReferenceDigest are hashes used to
// revoke by value and to resolve refresh and reference tokens. A zero
// ExpiresAt means the token never expires.
type Token struct {
	ID          string
	Subject     string
	Scope       kernel.Scope
	Audience    string
	Description string
	ProjectKey  string
	// ProjectIncarnations binds every project named by this token's boundary
	// or role/management scopes to its lifetime at issuance. It is server-side
	// metadata, never a JWT claim or a public token response.
	ProjectIncarnations map[string]string `json:"-"`
	IssuedAt            time.Time
	ExpiresAt           time.Time
	Refreshable         bool
	Revocable           bool
	LastUsed            time.Time
	AccessDigest        []byte `json:"-"`
	RefreshDigest       []byte `json:"-"`
	ReferenceDigest     []byte `json:"-"`
	// Purpose marks a token with a special role; empty for an ordinary one.
	// A subject has at most one token of each purpose.
	Purpose string
	// Secret is the reference token of a purposed token, kept so it can be
	// shown again; stores seal it. Empty for an ordinary token.
	Secret string `json:"-"`
	// Identity is how the subject resolves to a user.
	Identity kernel.TokenIdentity
	// Registry is set on a registry token alone, which the Docker token
	// endpoint issues and nothing stores (decision 0005): what it grants.
	Registry *kernel.RegistryAccess `json:"-"`
	// Parent is, on a registry token obtained with a token, the stored
	// token it descends from; revoking that token revokes it.
	Parent string `json:"-"`
}

// Expired reports whether the token has expired at now.
func (t Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

// ProjectKeys returns every project whose lifetime bounds the stored token.
// Management scopes bind a lifetime even though they grant no content action.
func (t Token) ProjectKeys() []string {
	var out []string
	if t.ProjectKey != "" {
		out = append(out, t.ProjectKey)
	}
	for _, e := range t.Scope.Entries {
		if e.Kind == kernel.ScopeRoles || e.Kind == kernel.ScopeProject {
			out = append(out, e.Project)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Revocable applies Access's rule: a token is revocable when it never
// expires, lives at least the revocable threshold, the caller forced it, or
// it is refreshable ("created with force_revocable: true or refreshable:
// true", revoketokenbyvalue). Other short-lived tokens are not tracked for
// revocation because they expire before a revocation list would matter; a
// refreshable one outlives its expiry through its refresh token.
func Revocable(expiresIn, threshold time.Duration, force, refreshable bool) bool {
	return force || refreshable || expiresIn == 0 || expiresIn >= threshold
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
