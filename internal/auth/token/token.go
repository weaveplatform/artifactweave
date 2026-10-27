// Package token issues and verifies Access tokens (decision 0005): RS256
// JWTs signed with a per-instance key, reference tokens and refresh tokens.
// Only digests of tokens are stored; the store record (token_id, subject,
// scope, expiry) is what revocation removes.
package token

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	// ErrInvalidRequest reports an issue request the service refuses.
	ErrInvalidRequest = errors.New("token: invalid token request")
	// ErrNeverExpiresAdminOnly refuses a token that never expires to a
	// caller who is not an administrator. It wraps ErrInvalidRequest, which
	// the OIDC exchange answers 400; the token API tests for it first and
	// answers 403, as createtoken documents a refused expires_in.
	ErrNeverExpiresAdminOnly = fmt.Errorf(
		"%w: only an administrator may issue a token that never expires",
		ErrInvalidRequest,
	)
	// ErrNotRevocable refuses to revoke a token shorter than the revocable
	// threshold that was neither forced revocable nor refreshable.
	ErrNotRevocable = errors.New("token: token is not revocable")
	// ErrRefreshOtherUser refuses a refresh token of another user than the
	// one refreshing: "The authenticated user must match the user of the
	// access token being refreshed" (createtoken.json, which answers 403).
	ErrRefreshOtherUser = errors.New("token: the refresh token belongs to another user")

	errMalformed = fmt.Errorf("%w: malformed", auth.ErrInvalidToken)
	errBadKey    = errors.New("token: bad signing key")
)

const (
	// DefaultAudience is the audience Access gives a token issued without one.
	DefaultAudience = "*@*"
	// touchEvery bounds how often verification writes LastUsed, so a busy
	// CLI does not turn every request into a write.
	touchEvery = time.Minute
	alphanum   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// Options configures New. Zero values mean the defaults.
type Options struct {
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// KeyBits is the RSA key size; zero means DefaultKeyBits. Only tests set
	// it, to keep key generation cheap.
	KeyBits int
}

// Service issues and verifies tokens over a store.
type Service struct {
	st     store.Store
	now    func() time.Time
	bits   int
	issuer string

	mu   sync.Mutex
	keys keyset
}

// New returns a Service. The instance identity must exist: the issuer is
// jfac@<the instance's id>. The first signing key is created when there is
// none.
func New(ctx context.Context, st store.Store, o Options) (*Service, error) {
	id, err := st.Instance().Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: instance identity: %w", err)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{
		st: st, now: o.Now, bits: cmp.Or(o.KeyBits, DefaultKeyBits),
		issuer: "jfac@" + strings.TrimPrefix(id.InstanceID, "jfrt@"),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	if s.keys.active == "" {
		if err := s.createKey(ctx); err != nil {
			return nil, err
		}
		if err := s.reload(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Issuer is this instance's Access service id, the iss of its tokens.
func (s *Service) Issuer() string { return s.issuer }

func (s *Service) reload(ctx context.Context) error {
	ks, err := s.load(ctx)
	if err != nil {
		return err
	}
	s.keys = ks
	return nil
}

// Request asks for a token for a user.
type Request struct {
	Username string
	// Scope defaults to applied-permissions/user.
	Scope types.Scope
	// ExpiresIn defaults to token.default_expiry (types.DefaultTokenExpiry
	// when unset); zero means never and needs ByAdmin.
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
	// Purpose marks a token its user holds at most one of
	// (types.TokenPurposeEncryptedPassword). Such a token always has a
	// reference token, and keeps it sealed so it can be shown again.
	Purpose string
	// Identity is how the token's subject resolves to a user. An OIDC token
	// exchange, and an administrator minting for a name that is no stored
	// user, issue a token for a user who need not be stored.
	Identity types.TokenIdentity
	// NotAfter, when set, is the latest the token may expire; a longer or
	// unending lifetime is cut to it. A transient user's token for itself
	// sets it to the expiry of the token that minted it: the user exists
	// only through that token, so a token outliving it would outlast the
	// 24 hours an OIDC mapping allows.
	NotAfter time.Time
}

// Issued is a new token and its secrets, which are never stored and so
// cannot be shown again.
type Issued struct {
	Record         types.Token
	AccessToken    string
	RefreshToken   string
	ReferenceToken string
	// ExpiresIn is the lifetime in seconds; zero means never.
	ExpiresIn int64
}

// Issue creates a token.
func (s *Service) Issue(ctx context.Context, r Request) (Issued, error) {
	if r.Username == "" || strings.Contains(r.Username, "/") {
		return Issued{}, fmt.Errorf("%w: a user name is required", ErrInvalidRequest)
	}
	expiresIn, err := s.expiresIn(ctx, r.ExpiresIn)
	if err != nil {
		return Issued{}, err
	}
	if expiresIn == 0 && !r.ByAdmin {
		return Issued{}, ErrNeverExpiresAdminOnly
	}
	scope := r.Scope
	if len(scope.Entries) == 0 {
		scope = types.UserScope()
	}
	// Seconds: the claims carry whole seconds, and the record matches them.
	now := s.now().Truncate(time.Second)
	if !r.NotAfter.IsZero() {
		left := r.NotAfter.Sub(now).Truncate(time.Second)
		if left <= 0 {
			return Issued{}, fmt.Errorf("%w: the token would expire at once", ErrInvalidRequest)
		}
		if expiresIn == 0 || expiresIn > left {
			expiresIn = left
		}
	}
	rec := types.Token{
		ID:          newUUID(),
		Subject:     s.issuer + "/users/" + r.Username,
		Scope:       scope,
		Audience:    cmp.Or(r.Audience, DefaultAudience),
		Description: r.Description,
		ProjectKey:  r.ProjectKey,
		IssuedAt:    now,
		Refreshable: r.Refreshable,
		Revocable: types.Revocable(
			expiresIn, types.DefaultRevocableThreshold, r.ForceRevocable, r.Refreshable,
		),
		Purpose:  r.Purpose,
		Identity: r.Identity,
	}
	if expiresIn > 0 {
		rec.ExpiresAt = now.Add(expiresIn)
	}
	out := Issued{Record: rec, ExpiresIn: int64(expiresIn / time.Second)}
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	c := claims{
		Ver: "2", Sub: rec.Subject, Scp: scope.String(), Aud: rec.Audience, Iss: s.issuer,
		Iat: now.Unix(), Jti: rec.ID,
	}
	if !rec.ExpiresAt.IsZero() {
		c.Exp = rec.ExpiresAt.Unix()
	}
	if out.AccessToken, err = sign(keys.priv, keys.active, c); err != nil {
		return Issued{}, err
	}
	rec.AccessDigest = digest(out.AccessToken)
	if r.Refreshable {
		out.RefreshToken = newUUID()
		rec.RefreshDigest = digest(out.RefreshToken)
	}
	if r.IncludeReference || r.Purpose != "" {
		out.ReferenceToken = newReference()
		rec.ReferenceDigest = digest(out.ReferenceToken)
	}
	if r.Purpose != "" {
		rec.Secret = out.ReferenceToken
	}
	if err := s.st.Tokens().Create(ctx, rec); err != nil {
		return Issued{}, fmt.Errorf("token: store: %w", err)
	}
	out.Record = rec
	return out, nil
}

// RegistryRequest asks for a registry token (decision 0005).
type RegistryRequest struct {
	Username string
	// ExpiresIn is the lifetime; it must be positive, and at most
	// types.RegistryTokenExpiry.
	ExpiresIn time.Duration
	Access    types.RegistryAccess
	// Scope is the scope of the credential that obtained the token, which
	// the token keeps so it never acts more broadly; it defaults to
	// applied-permissions/user, a password's.
	Scope types.Scope
	// Identity is that credential's identity when it came from an OIDC
	// exchange, so the token resolves to the same transient or synthetic
	// user it does.
	Identity types.TokenIdentity
	// Parent is the id of the stored token that obtained it, empty for a
	// password or anonymous caller. The token is refused once the parent is
	// revoked.
	Parent string
}

// IssueRegistry signs a registry token: an access token with the user's
// applied permissions whose registry claim carries what it grants. Nothing
// stores it, as Artifactory stores no token below its persistence
// threshold, so it cannot be listed or revoked; its short life, the
// grants and the user's current permissions bound it instead (Verify).
func (s *Service) IssueRegistry(_ context.Context, r RegistryRequest) (Issued, error) {
	if r.Username == "" || strings.Contains(r.Username, "/") {
		return Issued{}, fmt.Errorf("%w: a user name is required", ErrInvalidRequest)
	}
	if r.ExpiresIn <= 0 || r.ExpiresIn > types.RegistryTokenExpiry {
		return Issued{}, fmt.Errorf("%w: a registry token lives at most %s",
			ErrInvalidRequest, types.RegistryTokenExpiry)
	}
	if !knownIdentity(r.Identity) {
		return Issued{}, fmt.Errorf("%w: unknown identity %q", ErrInvalidRequest, r.Identity)
	}
	now := s.now().Truncate(time.Second)
	access := r.Access
	if access.Grants == nil {
		// An empty list, not null, in the claim, as the Docker token
		// specification writes it.
		access.Grants = []types.RegistryGrant{}
	}
	scope := r.Scope
	if len(scope.Entries) == 0 {
		scope = types.UserScope()
	}
	rec := types.Token{
		ID:        newUUID(),
		Subject:   s.issuer + "/users/" + r.Username,
		Scope:     scope,
		Audience:  DefaultAudience,
		IssuedAt:  now,
		ExpiresAt: now.Add(r.ExpiresIn),
		Identity:  r.Identity,
		Registry:  &access,
		Parent:    r.Parent,
	}
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	raw, err := sign(keys.priv, keys.active, claims{
		Ver: "2", Sub: rec.Subject, Scp: rec.Scope.String(), Aud: rec.Audience, Iss: s.issuer,
		Iat: now.Unix(), Exp: rec.ExpiresAt.Unix(), Jti: rec.ID, Registry: &access,
		Idn: r.Identity, Ptk: r.Parent,
	})
	if err != nil {
		return Issued{}, err
	}
	return Issued{Record: rec, AccessToken: raw, ExpiresIn: int64(r.ExpiresIn / time.Second)}, nil
}

// expiresIn resolves a requested lifetime.
func (s *Service) expiresIn(ctx context.Context, req *time.Duration) (time.Duration, error) {
	if req != nil {
		if *req < 0 {
			return 0, fmt.Errorf("%w: expires_in must not be negative", ErrInvalidRequest)
		}
		return *req, nil
	}
	// token.default_expiry is whole seconds, as the Access configuration
	// API carries it.
	var secs int64
	err := s.st.Settings().Get(ctx, types.SettingDefaultTokenExpiry, &secs)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return types.DefaultTokenExpiry, nil
	case err != nil:
		return 0, fmt.Errorf("token: default expiry: %w", err)
	case secs < 0:
		return types.DefaultTokenExpiry, nil
	}
	return time.Duration(secs) * time.Second, nil
}

// Verify resolves an access token (a JWT) or a reference token to its
// record. A token that is malformed, badly signed, signed by an unknown or
// retired key, expired, or revoked (no record) fails with an error wrapping
// auth.ErrInvalidToken.
func (s *Service) Verify(ctx context.Context, raw string) (types.Token, error) {
	var rec types.Token
	var err error
	if types.IsReferenceToken(raw) {
		rec, err = s.byDigest(ctx, store.DigestReference, raw)
	} else {
		rec, err = s.verifyJWT(ctx, raw, false)
	}
	if err != nil {
		return types.Token{}, err
	}
	now := s.now()
	if rec.Expired(now) {
		return types.Token{}, fmt.Errorf("%w: expired", auth.ErrInvalidToken)
	}
	if rec.Registry == nil && now.Sub(rec.LastUsed) >= touchEvery {
		// Best effort: a failed write must not fail the request.
		_ = s.st.Tokens().Touch(ctx, rec.ID, now)
	}
	return rec, nil
}

// VerifyExpired resolves an access token (a JWT) as Verify does, but
// accepts one whose only fault is its expiry. Signature, key, revocation and
// subject are all still checked; a reference token always fails. It serves
// only the refresh grant, which the JFrog CLI sends with the access token it
// is replacing, by then often expired. The record outlives its expiry for
// types.DefaultRefreshGrace (the gc keeps it), which bounds how long after
// expiry this succeeds.
func (s *Service) VerifyExpired(ctx context.Context, raw string) (types.Token, error) {
	if types.IsReferenceToken(raw) {
		return types.Token{}, fmt.Errorf("%w: not an access token", auth.ErrInvalidToken)
	}
	return s.verifyJWT(ctx, raw, true)
}

func (s *Service) verifyJWT(
	ctx context.Context,
	raw string,
	allowExpired bool,
) (types.Token, error) {
	p, err := parse(raw)
	if err != nil {
		return types.Token{}, err
	}
	if p.header.Alg != algRS256 {
		return types.Token{}, fmt.Errorf("%w: algorithm %q", auth.ErrInvalidToken, p.header.Alg)
	}
	key, err := s.publicKey(ctx, p.header.Kid)
	if err != nil {
		return types.Token{}, err
	}
	if !p.verify(key) {
		return types.Token{}, fmt.Errorf("%w: signature", auth.ErrInvalidToken)
	}
	var c claims
	if err := json.Unmarshal(p.payload, &c); err != nil {
		return types.Token{}, errMalformed
	}
	if !allowExpired && c.Exp != 0 && !s.now().Before(time.Unix(c.Exp, 0)) {
		return types.Token{}, fmt.Errorf("%w: expired", auth.ErrInvalidToken)
	}
	if c.Registry != nil {
		return s.registryRecord(ctx, c, allowExpired)
	}
	rec, err := s.st.Tokens().Get(ctx, c.Jti)
	if errors.Is(err, store.ErrNotFound) {
		return types.Token{}, fmt.Errorf("%w: revoked", auth.ErrInvalidToken)
	}
	if err != nil {
		return types.Token{}, fmt.Errorf("token: %w", err)
	}
	if rec.Subject != c.Sub {
		return types.Token{}, fmt.Errorf("%w: subject", auth.ErrInvalidToken)
	}
	return rec, nil
}

// registryRecord is the record a verified registry token stands for: no
// store holds one, but the token it descends from must still be stored,
// so revoking that token revokes this one.
func (s *Service) registryRecord(
	ctx context.Context,
	c claims,
	allowExpired bool,
) (types.Token, error) {
	rec, err := registryClaims(c, allowExpired)
	if err != nil || rec.Parent == "" {
		return rec, err
	}
	_, err = s.st.Tokens().Get(ctx, rec.Parent)
	if errors.Is(err, store.ErrNotFound) {
		return types.Token{}, fmt.Errorf("%w: its parent token was revoked", auth.ErrInvalidToken)
	}
	if err != nil {
		return types.Token{}, fmt.Errorf("token: parent of a registry token: %w", err)
	}
	return rec, nil
}

// registryClaims is the record a registry token's claims describe. A
// registry token never refreshes, and one without an expiry was not issued
// here.
func registryClaims(c claims, allowExpired bool) (types.Token, error) {
	if allowExpired || c.Exp == 0 {
		return types.Token{}, fmt.Errorf("%w: registry token", auth.ErrInvalidToken)
	}
	scope, err := types.ParseScope(c.Scp)
	if err != nil || !knownIdentity(c.Idn) {
		return types.Token{}, errMalformed
	}
	return types.Token{
		ID: c.Jti, Subject: c.Sub, Scope: scope, Audience: c.Aud,
		IssuedAt: time.Unix(c.Iat, 0), ExpiresAt: time.Unix(c.Exp, 0), Registry: c.Registry,
		Identity: c.Idn, Parent: c.Ptk,
	}, nil
}

// knownIdentity reports whether id is a stored user's or one an OIDC
// exchange issues.
func knownIdentity(id types.TokenIdentity) bool {
	return id == types.IdentityStored || id == types.IdentityTransient ||
		id == types.IdentitySynthetic
}

// publicKey returns the key with kid, reloading once when it is unknown,
// because another instance may have rotated.
func (s *Service) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys.public[kid]; ok {
		return k, nil
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	if k, ok := s.keys.public[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key", auth.ErrInvalidToken)
}

func (s *Service) byDigest(
	ctx context.Context,
	kind store.DigestKind,
	raw string,
) (types.Token, error) {
	rec, err := s.st.Tokens().ByDigest(ctx, kind, digest(raw))
	if errors.Is(err, store.ErrNotFound) {
		return types.Token{}, fmt.Errorf("%w: unknown token", auth.ErrInvalidToken)
	}
	if err != nil {
		return types.Token{}, fmt.Errorf("token: %w", err)
	}
	return rec, nil
}

// Refresh exchanges a refresh token for a new token with the same subject,
// scope, lifetime and options, and revokes the old one. A refresh token
// stays usable for types.DefaultRefreshGrace after its token expires.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Issued, error) {
	return s.RefreshAs(ctx, refreshToken, RefreshCheck{})
}

// RefreshCheck is what a refresh must match beyond a valid refresh token.
type RefreshCheck struct {
	// Username, when set, must be the refreshed token's user, or the refresh
	// fails with ErrRefreshOtherUser.
	Username string
	// TokenID, when set, must be the refreshed token's id, or the refresh
	// fails as an invalid refresh token: the caller presented that token as
	// its credential, and it must not vouch for refreshing another.
	TokenID string
}

// RefreshAs is Refresh for a refresh token that must pass c. The user is
// checked before the token, so another user's refresh token is
// ErrRefreshOtherUser whichever token the caller presented.
func (s *Service) RefreshAs(
	ctx context.Context,
	refreshToken string,
	c RefreshCheck,
) (Issued, error) {
	var out Issued
	err := s.st.Tx(ctx, func(ctx context.Context) error {
		old, err := s.byDigest(ctx, store.DigestRefresh, refreshToken)
		if err != nil {
			return err
		}
		if !old.ExpiresAt.IsZero() &&
			!s.now().Before(old.ExpiresAt.Add(types.DefaultRefreshGrace)) {
			return fmt.Errorf("%w: refresh token expired", auth.ErrInvalidToken)
		}
		sub, err := types.ParseSubject(old.Subject)
		if err != nil {
			return fmt.Errorf("%w: %w", auth.ErrInvalidToken, err)
		}
		if c.Username != "" && sub.Name != c.Username {
			return ErrRefreshOtherUser
		}
		if c.TokenID != "" && old.ID != c.TokenID {
			return fmt.Errorf("%w: refresh token of another token", auth.ErrInvalidToken)
		}
		var lifetime time.Duration
		if !old.ExpiresAt.IsZero() {
			lifetime = old.ExpiresAt.Sub(old.IssuedAt)
		}
		if err := s.st.Tokens().Delete(ctx, old.ID); err != nil {
			return fmt.Errorf("token: revoke: %w", err)
		}
		out, err = s.Issue(ctx, Request{
			Username: sub.Name, Scope: old.Scope, ExpiresIn: &lifetime, Refreshable: true,
			ForceRevocable: old.Revocable, Audience: old.Audience, Description: old.Description,
			ProjectKey: old.ProjectKey, IncludeReference: len(old.ReferenceDigest) > 0,
			// The lifetime was allowed when the token was first issued.
			ByAdmin: true,
			// A transient user's token stays one: as a stored user's, the
			// refreshed token would answer 401 for want of a user record.
			Identity: old.Identity,
		})
		return err
	})
	if err != nil {
		return Issued{}, fmt.Errorf("token: refresh: %w", err)
	}
	return out, nil
}

// EncryptedPassword returns the user's encrypted password: the reference
// token of their types.TokenPurposeEncryptedPassword token, which never
// expires. A user holds at most one, created on first use and shown again
// after, so every client given it keeps working until the password changes
// and the identity service revokes it.
func (s *Service) EncryptedPassword(ctx context.Context, username string) (string, error) {
	if secret, err := s.encryptedPassword(ctx, username); err != nil || secret != "" {
		return secret, err
	}
	never := time.Duration(0)
	issued, err := s.Issue(ctx, Request{
		Username: username, ExpiresIn: &never, ForceRevocable: true,
		Description: "encrypted password", Purpose: types.TokenPurposeEncryptedPassword,
		// Never expiring is otherwise admin only; this token stands in for
		// the user's own password, which does not expire either.
		ByAdmin: true,
	})
	if errors.Is(err, store.ErrConflict) {
		// A concurrent first use created it: answer with that one, so both
		// callers hold the same value.
		secret, rerr := s.encryptedPassword(ctx, username)
		if rerr != nil || secret != "" {
			return secret, rerr
		}
	}
	if err != nil {
		return "", fmt.Errorf("token: encrypted password: %w", err)
	}
	return issued.ReferenceToken, nil
}

// encryptedPassword returns the user's stored encrypted password, or "".
func (s *Service) encryptedPassword(ctx context.Context, username string) (string, error) {
	ts, _, err := s.st.Tokens().List(ctx, store.TokenFilter{
		Subject: s.issuer + "/users/" + username, Purpose: types.TokenPurposeEncryptedPassword,
	}, store.Page{})
	if err != nil {
		return "", fmt.Errorf("token: encrypted password: %w", err)
	}
	if len(ts) == 0 {
		return "", nil
	}
	return ts[0].Secret, nil
}

// Revoke revokes the token with id.
func (s *Service) Revoke(ctx context.Context, id string) error {
	rec, err := s.st.Tokens().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	return s.revoke(ctx, rec)
}

// RevokeValue revokes the token whose access or reference token is raw.
func (s *Service) RevokeValue(ctx context.Context, raw string) error {
	kind := store.DigestAccess
	if types.IsReferenceToken(raw) {
		kind = store.DigestReference
	}
	rec, err := s.st.Tokens().ByDigest(ctx, kind, digest(raw))
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	return s.revoke(ctx, rec)
}

func (s *Service) revoke(ctx context.Context, rec types.Token) error {
	if !rec.Revocable {
		return ErrNotRevocable
	}
	if err := s.st.Tokens().Delete(ctx, rec.ID); err != nil {
		return fmt.Errorf("token: revoke: %w", err)
	}
	return nil
}

func digest(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// newUUID returns a random (version 4) UUID, the form of Access token ids.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// newReference returns a reference token: "cmVmd" (base64 of "ref") and 59
// random alphanumerics, the 64-character form clients recognise.
func newReference() string {
	out := make([]byte, 0, 64)
	out = append(out, "cmVmd"...)
	var b [1]byte
	for len(out) < 64 {
		_, _ = rand.Read(b[:])
		// 248 = 4*62: rejecting the rest keeps every character equally likely.
		if b[0] < 248 {
			out = append(out, alphanum[b[0]%62])
		}
	}
	return string(out)
}
