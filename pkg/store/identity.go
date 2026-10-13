package store

import (
	"context"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
)

// UserStore persists users. A user's Groups are its memberships, kept in
// the group_members table shared with GroupStore.
type UserStore interface {
	// Create stores u and its memberships; ErrConflict when the name exists,
	// ErrNotFound when a named group does not.
	Create(ctx context.Context, u types.User) error
	// Get returns the user with Groups sorted.
	Get(ctx context.Context, name string) (types.User, error)
	// Update replaces the user's fields and memberships.
	Update(ctx context.Context, u types.User) error
	// Delete removes the user and its memberships.
	Delete(ctx context.Context, name string) error
	// List returns users ordered by name.
	List(ctx context.Context, p Page) ([]types.User, string, error)
	// RecordLogin sets LastLoggedIn.
	RecordLogin(ctx context.Context, name string, at time.Time) error
}

// GroupStore persists groups.
type GroupStore interface {
	// Create stores g and its members; ErrConflict when the name exists,
	// ErrNotFound when a member does not.
	Create(ctx context.Context, g types.Group) error
	// Get returns the group with Members sorted.
	Get(ctx context.Context, name string) (types.Group, error)
	// Update replaces the group's fields and members.
	Update(ctx context.Context, g types.Group) error
	// Delete removes the group and its memberships.
	Delete(ctx context.Context, name string) error
	// List returns groups ordered by name.
	List(ctx context.Context, p Page) ([]types.Group, string, error)
	// AddMembers adds users to the group; adding a member twice is not an
	// error. ErrNotFound when the group or a user does not exist.
	AddMembers(ctx context.Context, group string, users ...string) error
	// RemoveMembers removes users from the group; removing a non-member is
	// not an error.
	RemoveMembers(ctx context.Context, group string, users ...string) error
	// AutoJoin returns the names of groups new users join, ordered.
	AutoJoin(ctx context.Context) ([]string, error)
}

// PermissionStore persists permissions in the Access v2 shape (decision
// 0006). Grants and targets are mirrored into their own tables so the
// authorizer looks permissions up by principal and by target.
type PermissionStore interface {
	// Create stores p; ErrConflict when the name exists.
	Create(ctx context.Context, p types.Permission) error
	// Get returns the permission.
	Get(ctx context.Context, name string) (types.Permission, error)
	// Update replaces the permission.
	Update(ctx context.Context, p types.Permission) error
	// Delete removes the permission.
	Delete(ctx context.Context, name string) error
	// List returns permissions ordered by name.
	List(ctx context.Context, p Page) ([]types.Permission, string, error)
	// ForPrincipal returns the permissions that grant any action to user or
	// to any of groups, ordered by name.
	ForPrincipal(ctx context.Context, user string, groups []string) ([]types.Permission, error)
	// WithTarget returns the permissions naming target (a repository key, a
	// build repository, or an ANY target) in any resource, ordered by name.
	WithTarget(ctx context.Context, target string) ([]types.Permission, error)
}

// DigestKind selects which token digest ByDigest matches.
type DigestKind int

// Token digests.
const (
	DigestAccess DigestKind = iota + 1
	DigestRefresh
	DigestReference
)

// TokenFilter narrows token listings.
type TokenFilter struct {
	Subject string
}

// TokenStore persists access token records; the tokens themselves are never
// stored.
type TokenStore interface {
	// Create stores t; ErrConflict when the ID or a digest exists.
	Create(ctx context.Context, t types.Token) error
	// Get returns the token with id.
	Get(ctx context.Context, id string) (types.Token, error)
	// ByDigest returns the token whose digest of the given kind is digest,
	// which is how revocation by value and refresh find the record.
	ByDigest(ctx context.Context, kind DigestKind, digest []byte) (types.Token, error)
	// List returns tokens ordered by (IssuedAt, ID).
	List(ctx context.Context, f TokenFilter, p Page) ([]types.Token, string, error)
	// Delete removes the token, revoking it.
	Delete(ctx context.Context, id string) error
	// Touch sets LastUsed.
	Touch(ctx context.Context, id string, at time.Time) error
	// DeleteExpired removes tokens that expired before t and returns how
	// many; tokens that never expire are kept.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// KeyStore persists key material: repository key pairs, trusted CA
// certificates, trusted release-bundle keys and Distribution signing keys.
// Private keys and passphrases are sealed.
type KeyStore interface {
	// CreateKeypair stores k; ErrConflict when the name exists.
	CreateKeypair(ctx context.Context, k types.Keypair) error
	// Keypair returns the key pair called name.
	Keypair(ctx context.Context, name string) (types.Keypair, error)
	// Keypairs returns every key pair ordered by name.
	Keypairs(ctx context.Context) ([]types.Keypair, error)
	// UpdateKeypair replaces a key pair.
	UpdateKeypair(ctx context.Context, k types.Keypair) error
	// DeleteKeypair removes a key pair. Refusing to remove one a repository
	// still uses (RepoStore.UsingKeypair) is the caller's decision.
	DeleteKeypair(ctx context.Context, name string) error

	// PutCertificate creates or replaces the certificate with c.Alias.
	PutCertificate(ctx context.Context, c types.Certificate) error
	// Certificate returns the certificate with alias.
	Certificate(ctx context.Context, alias string) (types.Certificate, error)
	// Certificates returns every certificate ordered by alias.
	Certificates(ctx context.Context) ([]types.Certificate, error)
	// DeleteCertificate removes a certificate.
	DeleteCertificate(ctx context.Context, alias string) error

	// CreateTrustedKey stores k; ErrConflict when the kid or alias exists.
	CreateTrustedKey(ctx context.Context, k types.TrustedKey) error
	// TrustedKey returns the trusted key with kid.
	TrustedKey(ctx context.Context, kid string) (types.TrustedKey, error)
	// TrustedKeys returns every trusted key ordered by kid.
	TrustedKeys(ctx context.Context) ([]types.TrustedKey, error)
	// DeleteTrustedKey removes a trusted key.
	DeleteTrustedKey(ctx context.Context, kid string) error

	// PutSigningKey creates or replaces the signing key with k.Alias. A key
	// stored with IsDefault clears the flag on every other key.
	PutSigningKey(ctx context.Context, k types.SigningKey) error
	// SigningKey returns the signing key with alias.
	SigningKey(ctx context.Context, alias string) (types.SigningKey, error)
	// SigningKeys returns every signing key ordered by alias.
	SigningKeys(ctx context.Context) ([]types.SigningKey, error)
	// DefaultSigningKey returns the key marked IsDefault.
	DefaultSigningKey(ctx context.Context) (types.SigningKey, error)
	// DeleteSigningKey removes a signing key.
	DeleteSigningKey(ctx context.Context, alias string) error
}
