package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Seal purposes of the key material columns (decision 0017). The row id is
// the key pair name or signing key alias.
//
//nolint:gosec // G101: column names, not credentials
const (
	purposeKeypairPrivate    = "keypairs.private_key"
	purposeKeypairPassphrase = "keypairs.passphrase"
	purposeSigningPrivate    = "signing_keys.private_key"
	purposeSigningPassphrase = "signing_keys.passphrase"
	purposeCertificateKey    = "certificates.private_key"
)

// Tokens implements store.Store.
func (s *Store) Tokens() store.TokenStore { return tokens{s} }

// Keys implements store.Store.
func (s *Store) Keys() store.KeyStore { return keys{s} }

// --- Tokens ----------------------------------------------------------------

type tokens struct{ s *Store }

const tokenCols = "id, subject, access_digest, refresh_digest, reference_digest, scope, audience, " +
	"description, project_key, issued_at, expires_at, refreshable, revocable, last_used_at"

// tokenKey is the keyset of the token list; issued_at is never NULL (0 for
// an unset time) so both engines sort and compare it the same way.
var tokenKey = []string{"issued_at", "id"}

// issuedMicros is the issued_at column value: 0 for an unset time.
func issuedMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// digestArg is a digest column value: NULL when empty, so unset digests never
// collide on the unique indexes.
func digestArg(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func scanToken(row interface{ Scan(...any) error }) (types.Token, error) {
	var (
		t                types.Token
		scope            string
		issued           int64
		expires, used    sql.NullInt64
		access, ref, rfr []byte
	)
	if err := row.Scan(
		&t.ID,
		&t.Subject,
		&access,
		&rfr,
		&ref,
		&scope,
		&t.Audience,
		&t.Description,
		&t.ProjectKey,
		&issued,
		&expires,
		&t.Refreshable,
		&t.Revocable,
		&used,
	); err != nil {
		return t, err //nolint:wrapcheck // wrapped by the caller
	}
	if len(access) > 0 {
		t.AccessDigest = access
	}
	if len(rfr) > 0 {
		t.RefreshDigest = rfr
	}
	if len(ref) > 0 {
		t.ReferenceDigest = ref
	}
	if issued != 0 {
		t.IssuedAt = time.UnixMicro(issued).UTC()
	}
	t.ExpiresAt, t.LastUsed = fromMicros(expires), fromMicros(used)
	if scope != "" {
		sc, err := types.ParseScope(scope)
		if err != nil {
			return t, fmt.Errorf("token %q: %w", t.ID, err)
		}
		t.Scope = sc
	}
	return t, nil
}

func (ts tokens) Create(ctx context.Context, t types.Token) error {
	_, err := ts.s.exec(
		ctx,
		"create token "+t.ID,
		Insert("tokens", strings.Split(tokenCols, ", ")),
		t.ID,
		t.Subject,
		digestArg(t.AccessDigest),
		digestArg(t.RefreshDigest),
		digestArg(t.ReferenceDigest),
		t.Scope.String(),
		t.Audience,
		t.Description,
		t.ProjectKey,
		issuedMicros(t.IssuedAt),
		micros(t.ExpiresAt),
		t.Refreshable,
		t.Revocable,
		micros(t.LastUsed),
	)
	return err
}

func (ts tokens) Get(ctx context.Context, id string) (types.Token, error) {
	t, err := scanToken(ts.s.q(ctx).QueryRowContext(ctx,
		ts.s.d.Rebind("SELECT "+tokenCols+" FROM tokens WHERE id = ?"), id))
	return t, ts.s.wrap(fmt.Sprintf("get token %q", id), err)
}

func (ts tokens) ByDigest(
	ctx context.Context,
	kind store.DigestKind,
	d []byte,
) (types.Token, error) {
	var col string
	switch kind {
	case store.DigestAccess:
		col = "access_digest"
	case store.DigestRefresh:
		col = "refresh_digest"
	case store.DigestReference:
		col = "reference_digest"
	default:
		return types.Token{}, fmt.Errorf("token digest kind %d: %w", kind, store.ErrInvalid)
	}
	if len(d) == 0 {
		return types.Token{}, fmt.Errorf("token by empty %s: %w", col, store.ErrNotFound)
	}
	t, err := scanToken(ts.s.q(ctx).QueryRowContext(ctx,
		ts.s.d.Rebind("SELECT "+tokenCols+" FROM tokens WHERE "+col+" = ?"), d))
	return t, ts.s.wrap("get token by "+col, err)
}

func (ts tokens) List(
	ctx context.Context,
	f store.TokenFilter,
	p store.Page,
) ([]types.Token, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, tokenKey, map[string]bool{"issued_at": true}, false)
	if err != nil {
		return nil, "", err
	}
	if f.Subject != "" {
		cond += " AND subject = ?"
		args = append(args, f.Subject)
	}
	var out []types.Token
	err = ts.s.rows(ctx, "list tokens",
		"SELECT "+tokenCols+" FROM tokens WHERE "+cond+orderBy(tokenKey, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			t, err := scanToken(r)
			out = append(out, t)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(t types.Token) []string {
		return []string{itoa(issuedMicros(t.IssuedAt)), t.ID}
	})
	return page, next, nil
}

func (ts tokens) Delete(ctx context.Context, id string) error {
	return ts.s.execOne(
		ctx,
		fmt.Sprintf("delete token %q", id),
		"DELETE FROM tokens WHERE id = ?",
		id,
	)
}

func (ts tokens) Touch(ctx context.Context, id string, at time.Time) error {
	return ts.s.execOne(ctx, fmt.Sprintf("touch token %q", id),
		"UPDATE tokens SET last_used_at = ? WHERE id = ?", micros(at), id)
}

func (ts tokens) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	return ts.s.exec(ctx, "delete expired tokens",
		"DELETE FROM tokens WHERE expires_at IS NOT NULL AND expires_at < ?", before.UnixMicro())
}

// --- Keys ------------------------------------------------------------------

type keys struct{ s *Store }

const keypairCols = "name, type, alias, public_key, private_key, passphrase"

func (ks keys) keypairArgs(k types.Keypair) ([]any, error) {
	priv, err := ks.s.seal(purposeKeypairPrivate, k.Name, k.PrivateKey)
	if err != nil {
		return nil, err
	}
	pass, err := ks.s.seal(purposeKeypairPassphrase, k.Name, k.Passphrase)
	if err != nil {
		return nil, err
	}
	return []any{k.Name, string(k.Type), k.Alias, k.PublicKey, priv, pass}, nil
}

func (ks keys) scanKeypair(row interface{ Scan(...any) error }) (types.Keypair, error) {
	var (
		k          types.Keypair
		typ        string
		priv, pass []byte
	)
	if err := row.Scan(&k.Name, &typ, &k.Alias, &k.PublicKey, &priv, &pass); err != nil {
		return k, err //nolint:wrapcheck // wrapped by the caller
	}
	k.Type = types.KeypairType(typ)
	var err error
	if k.PrivateKey, err = ks.s.unseal(purposeKeypairPrivate, k.Name, priv); err != nil {
		return k, err
	}
	k.Passphrase, err = ks.s.unseal(purposeKeypairPassphrase, k.Name, pass)
	return k, err
}

func (ks keys) CreateKeypair(ctx context.Context, k types.Keypair) error {
	args, err := ks.keypairArgs(k)
	if err != nil {
		return err
	}
	_, err = ks.s.exec(
		ctx,
		"create keypair "+k.Name,
		Insert("keypairs", strings.Split(keypairCols, ", ")),
		args...)
	return err
}

func (ks keys) Keypair(ctx context.Context, name string) (types.Keypair, error) {
	k, err := ks.scanKeypair(ks.s.q(ctx).QueryRowContext(ctx,
		ks.s.d.Rebind("SELECT "+keypairCols+" FROM keypairs WHERE name = ?"), name))
	return k, ks.s.wrap(fmt.Sprintf("get keypair %q", name), err)
}

func (ks keys) Keypairs(ctx context.Context) ([]types.Keypair, error) {
	var out []types.Keypair
	err := ks.s.rows(
		ctx,
		"list keypairs",
		"SELECT "+keypairCols+" FROM keypairs ORDER BY name",
		nil,
		func(r *sql.Rows) error {
			k, err := ks.scanKeypair(r)
			out = append(out, k)
			return err
		},
	)
	return out, err
}

func (ks keys) UpdateKeypair(ctx context.Context, k types.Keypair) error {
	args, err := ks.keypairArgs(k)
	if err != nil {
		return err
	}
	return ks.s.execOne(
		ctx,
		fmt.Sprintf("update keypair %q", k.Name),
		"UPDATE keypairs SET type = ?, alias = ?, public_key = ?, private_key = ?, passphrase = ? WHERE name = ?",
		append(args[1:], k.Name)...)
}

func (ks keys) DeleteKeypair(ctx context.Context, name string) error {
	return ks.s.execOne(
		ctx,
		fmt.Sprintf("delete keypair %q", name),
		"DELETE FROM keypairs WHERE name = ?",
		name,
	)
}

const certificateCols = "alias, pem, private_key, issued_to, issued_by, issued_on, valid_until, fingerprint"

func (ks keys) scanCertificate(row interface{ Scan(...any) error }) (types.Certificate, error) {
	var (
		c         types.Certificate
		key       []byte
		on, until sql.NullInt64
	)
	if err := row.Scan(
		&c.Alias,
		&c.PEM,
		&key,
		&c.IssuedTo,
		&c.IssuedBy,
		&on,
		&until,
		&c.Fingerprint,
	); err != nil {
		return c, err //nolint:wrapcheck // wrapped by the caller
	}
	c.IssuedOn, c.ValidUntil = fromMicros(on), fromMicros(until)
	var err error
	c.PrivateKey, err = ks.s.unseal(purposeCertificateKey, c.Alias, key)
	return c, err
}

func (ks keys) PutCertificate(ctx context.Context, c types.Certificate) error {
	key, err := ks.s.seal(purposeCertificateKey, c.Alias, c.PrivateKey)
	if err != nil {
		return err
	}
	_, err = ks.s.exec(
		ctx,
		"put certificate "+c.Alias,
		Upsert("certificates", strings.Split(certificateCols, ", "), []string{"alias"}),
		c.Alias,
		c.PEM,
		key,
		c.IssuedTo,
		c.IssuedBy,
		micros(c.IssuedOn),
		micros(c.ValidUntil),
		c.Fingerprint,
	)
	return err
}

func (ks keys) Certificate(ctx context.Context, alias string) (types.Certificate, error) {
	c, err := ks.scanCertificate(ks.s.q(ctx).QueryRowContext(ctx,
		ks.s.d.Rebind("SELECT "+certificateCols+" FROM certificates WHERE alias = ?"), alias))
	return c, ks.s.wrap(fmt.Sprintf("get certificate %q", alias), err)
}

func (ks keys) Certificates(ctx context.Context) ([]types.Certificate, error) {
	var out []types.Certificate
	err := ks.s.rows(ctx, "list certificates",
		"SELECT "+certificateCols+" FROM certificates ORDER BY alias", nil,
		func(r *sql.Rows) error {
			c, err := ks.scanCertificate(r)
			out = append(out, c)
			return err
		})
	return out, err
}

func (ks keys) DeleteCertificate(ctx context.Context, alias string) error {
	return ks.s.execOne(ctx, fmt.Sprintf("delete certificate %q", alias),
		"DELETE FROM certificates WHERE alias = ?", alias)
}

const trustedKeyCols = "kid, alias, public_key, fingerprint, issued_on, issued_by, valid_until"

func scanTrustedKey(row interface{ Scan(...any) error }) (types.TrustedKey, error) {
	var (
		k         types.TrustedKey
		alias     sql.NullString
		on, until sql.NullInt64
	)
	if err := row.Scan(
		&k.Kid,
		&alias,
		&k.PublicKey,
		&k.Fingerprint,
		&on,
		&k.IssuedBy,
		&until,
	); err != nil {
		return k, err //nolint:wrapcheck // wrapped by the caller
	}
	k.Alias = alias.String
	k.IssuedOn, k.ValidUntil = fromMicros(on), fromMicros(until)
	return k, nil
}

func (ks keys) CreateTrustedKey(ctx context.Context, k types.TrustedKey) error {
	_, err := ks.s.exec(
		ctx,
		"create trusted key "+k.Kid,
		Insert("trusted_keys", strings.Split(trustedKeyCols, ", ")),
		k.Kid,
		nullString(k.Alias),
		k.PublicKey,
		k.Fingerprint,
		micros(k.IssuedOn),
		k.IssuedBy,
		micros(k.ValidUntil),
	)
	return err
}

func (ks keys) TrustedKey(ctx context.Context, kid string) (types.TrustedKey, error) {
	k, err := scanTrustedKey(ks.s.q(ctx).QueryRowContext(ctx,
		ks.s.d.Rebind("SELECT "+trustedKeyCols+" FROM trusted_keys WHERE kid = ?"), kid))
	return k, ks.s.wrap(fmt.Sprintf("get trusted key %q", kid), err)
}

func (ks keys) TrustedKeys(ctx context.Context) ([]types.TrustedKey, error) {
	var out []types.TrustedKey
	err := ks.s.rows(ctx, "list trusted keys",
		"SELECT "+trustedKeyCols+" FROM trusted_keys ORDER BY kid", nil,
		func(r *sql.Rows) error {
			k, err := scanTrustedKey(r)
			out = append(out, k)
			return err
		})
	return out, err
}

func (ks keys) DeleteTrustedKey(ctx context.Context, kid string) error {
	return ks.s.execOne(ctx, fmt.Sprintf("delete trusted key %q", kid),
		"DELETE FROM trusted_keys WHERE kid = ?", kid)
}

const signingKeyCols = "alias, public_key, private_key, passphrase, is_default"

func (ks keys) scanSigningKey(row interface{ Scan(...any) error }) (types.SigningKey, error) {
	var (
		k          types.SigningKey
		priv, pass []byte
	)
	if err := row.Scan(&k.Alias, &k.PublicKey, &priv, &pass, &k.IsDefault); err != nil {
		return k, err //nolint:wrapcheck // wrapped by the caller
	}
	var err error
	if k.PrivateKey, err = ks.s.unseal(purposeSigningPrivate, k.Alias, priv); err != nil {
		return k, err
	}
	k.Passphrase, err = ks.s.unseal(purposeSigningPassphrase, k.Alias, pass)
	return k, err
}

func (ks keys) PutSigningKey(ctx context.Context, k types.SigningKey) error {
	priv, err := ks.s.seal(purposeSigningPrivate, k.Alias, k.PrivateKey)
	if err != nil {
		return err
	}
	pass, err := ks.s.seal(purposeSigningPassphrase, k.Alias, k.Passphrase)
	if err != nil {
		return err
	}
	return ks.s.Tx(ctx, func(ctx context.Context) error {
		if k.IsDefault {
			if _, err := ks.s.exec(ctx, "clear default signing key",
				"UPDATE signing_keys SET is_default = ? WHERE alias <> ? AND is_default = ?",
				false, k.Alias, true); err != nil {
				return err
			}
		}
		_, err := ks.s.exec(ctx, "put signing key "+k.Alias,
			Upsert("signing_keys", strings.Split(signingKeyCols, ", "), []string{"alias"}),
			k.Alias, k.PublicKey, priv, pass, k.IsDefault)
		return err
	})
}

func (ks keys) signingKey(
	ctx context.Context,
	op, where string,
	arg any,
) (types.SigningKey, error) {
	k, err := ks.scanSigningKey(ks.s.q(ctx).QueryRowContext(ctx,
		ks.s.d.Rebind("SELECT "+signingKeyCols+" FROM signing_keys WHERE "+where+" ORDER BY alias LIMIT 1"),
		arg),
	)
	return k, ks.s.wrap(op, err)
}

func (ks keys) SigningKey(ctx context.Context, alias string) (types.SigningKey, error) {
	return ks.signingKey(ctx, fmt.Sprintf("get signing key %q", alias), "alias = ?", alias)
}

func (ks keys) DefaultSigningKey(ctx context.Context) (types.SigningKey, error) {
	return ks.signingKey(ctx, "get default signing key", "is_default = ?", true)
}

func (ks keys) SigningKeys(ctx context.Context) ([]types.SigningKey, error) {
	var out []types.SigningKey
	err := ks.s.rows(ctx, "list signing keys",
		"SELECT "+signingKeyCols+" FROM signing_keys ORDER BY alias", nil,
		func(r *sql.Rows) error {
			k, err := ks.scanSigningKey(r)
			out = append(out, k)
			return err
		})
	return out, err
}

func (ks keys) DeleteSigningKey(ctx context.Context, alias string) error {
	return ks.s.execOne(ctx, fmt.Sprintf("delete signing key %q", alias),
		"DELETE FROM signing_keys WHERE alias = ?", alias)
}
