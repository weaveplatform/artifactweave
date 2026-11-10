package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Seal purposes of the key material columns (decision 0017). The row id is
// the row's key: the key pair name, the signing key or certificate alias,
// or the token id.
//
//nolint:gosec // G101: column names, not credentials
const (
	purposeKeypairPrivate    = "keypairs.private_key"
	purposeKeypairPassphrase = "keypairs.passphrase"
	purposeSigningPrivate    = "signing_keys.private_key"
	purposeSigningPassphrase = "signing_keys.passphrase"
	purposeCertificateKey    = "certificates.private_key"
	purposeTokenSecret       = "tokens.secret"
)

// Tokens returns the token store: token records, with a purposed
// token's secret sealed (decisions 0005 and 0017).
func (s *Store) Tokens() port.TokenStore { return tokens{s} }

// Keys returns the key store: key pairs, certificates, trusted keys and
// signing keys, with private keys and passphrases sealed (decision
// 0017).
func (s *Store) Keys() port.KeyStore { return keys{s} }

// --- Tokens ----------------------------------------------------------------

type tokens struct{ s *Store }

const tokenCols = "id, subject, access_digest, refresh_digest, reference_digest, scope, audience, " +
	"description, project_key, issued_at, expires_at, refreshable, revocable, last_used_at, " +
	"purpose, secret, identity"

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

// scan reads a token row; the secret of a purposed token is sealed with the
// token id.
func (ts tokens) scan(row interface{ Scan(...any) error }) (model.Token, error) {
	var (
		t                model.Token
		scope            string
		issued           int64
		expires, used    sql.NullInt64
		access, ref, rfr []byte
		secret           []byte
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
		&t.Purpose,
		&secret,
		&t.Identity,
	); err != nil {
		return t, err //nolint:wrapcheck // wrapped by the caller
	}
	var err error
	if t.Secret, err = ts.s.Unseal(purposeTokenSecret, t.ID, secret); err != nil {
		return t, err
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
	t.ExpiresAt, t.LastUsed = dialect.FromMicros(expires), dialect.FromMicros(used)
	if scope != "" {
		sc, err := kernel.ParseScope(scope)
		if err != nil {
			return t, fmt.Errorf("token %q: %w", t.ID, err)
		}
		t.Scope = sc
	}
	return t, nil
}

func (ts tokens) Create(ctx context.Context, t model.Token) error {
	secret, err := ts.s.Seal(purposeTokenSecret, t.ID, t.Secret)
	if err != nil {
		return err
	}
	_, err = ts.s.Exec(
		ctx,
		"create token "+t.ID,
		dialect.Insert("tokens", strings.Split(tokenCols, ", ")),
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
		dialect.Micros(t.ExpiresAt),
		t.Refreshable,
		t.Revocable,
		dialect.Micros(t.LastUsed),
		t.Purpose,
		secret,
		string(t.Identity),
	)
	return err
}

func (ts tokens) Get(ctx context.Context, id string) (model.Token, error) {
	t, err := ts.scan(ts.s.Q(ctx).QueryRowContext(ctx,
		ts.s.Dialect().Rebind("SELECT "+tokenCols+" FROM tokens WHERE id = ?"), id))
	return t, ts.s.Wrap(fmt.Sprintf("get token %q", id), err)
}

func (ts tokens) ByDigest(
	ctx context.Context,
	kind port.DigestKind,
	d []byte,
) (model.Token, error) {
	var col string
	switch kind {
	case port.DigestAccess:
		col = "access_digest"
	case port.DigestRefresh:
		col = "refresh_digest"
	case port.DigestReference:
		col = "reference_digest"
	default:
		return model.Token{}, fmt.Errorf("token digest kind %d: %w", kind, kernel.ErrInvalid)
	}
	if len(d) == 0 {
		return model.Token{}, fmt.Errorf("token by empty %s: %w", col, kernel.ErrNotFound)
	}
	t, err := ts.scan(ts.s.Q(ctx).QueryRowContext(ctx,
		ts.s.Dialect().Rebind("SELECT "+tokenCols+" FROM tokens WHERE "+col+" = ?"), d))
	return t, ts.s.Wrap("get token by "+col, err)
}

func (ts tokens) List(
	ctx context.Context,
	f port.TokenFilter,
	p kernel.Page,
) ([]model.Token, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, tokenKey, map[string]bool{"issued_at": true}, false)
	if err != nil {
		return nil, "", err
	}
	if f.Subject != "" {
		cond += " AND subject = ?"
		args = append(args, f.Subject)
	}
	if f.Purpose != "" {
		cond += " AND purpose = ?"
		args = append(args, f.Purpose)
	}
	var out []model.Token
	err = ts.s.Rows(ctx, "list tokens",
		"SELECT "+tokenCols+" FROM tokens WHERE "+cond+dialect.OrderBy(tokenKey, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			t, err := ts.scan(r)
			out = append(out, t)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(out, p.Limit, func(t model.Token) []string {
		return []string{dialect.Itoa(issuedMicros(t.IssuedAt)), t.ID}
	})
	return page, next, nil
}

func (ts tokens) Delete(ctx context.Context, id string) error {
	return ts.s.ExecOne(
		ctx,
		fmt.Sprintf("delete token %q", id),
		"DELETE FROM tokens WHERE id = ?",
		id,
	)
}

func (ts tokens) Touch(ctx context.Context, id string, at time.Time) error {
	return ts.s.ExecOne(ctx, fmt.Sprintf("touch token %q", id),
		"UPDATE tokens SET last_used_at = ? WHERE id = ?", dialect.Micros(at), id)
}

func (ts tokens) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	return ts.s.Exec(ctx, "delete expired tokens",
		"DELETE FROM tokens WHERE expires_at IS NOT NULL AND expires_at < ?", before.UnixMicro())
}

// --- Keys ------------------------------------------------------------------

type keys struct{ s *Store }

const keypairCols = "name, type, alias, public_key, private_key, passphrase"

func (ks keys) keypairArgs(k api.Keypair) ([]any, error) {
	priv, err := ks.s.Seal(purposeKeypairPrivate, k.Name, k.PrivateKey)
	if err != nil {
		return nil, err
	}
	pass, err := ks.s.Seal(purposeKeypairPassphrase, k.Name, k.Passphrase)
	if err != nil {
		return nil, err
	}
	return []any{k.Name, string(k.Type), k.Alias, k.PublicKey, priv, pass}, nil
}

func (ks keys) scanKeypair(row interface{ Scan(...any) error }) (api.Keypair, error) {
	var (
		k          api.Keypair
		typ        string
		priv, pass []byte
	)
	if err := row.Scan(&k.Name, &typ, &k.Alias, &k.PublicKey, &priv, &pass); err != nil {
		return k, err //nolint:wrapcheck // wrapped by the caller
	}
	k.Type = api.KeypairType(typ)
	var err error
	if k.PrivateKey, err = ks.s.Unseal(purposeKeypairPrivate, k.Name, priv); err != nil {
		return k, err
	}
	k.Passphrase, err = ks.s.Unseal(purposeKeypairPassphrase, k.Name, pass)
	return k, err
}

func (ks keys) CreateKeypair(ctx context.Context, k api.Keypair) error {
	args, err := ks.keypairArgs(k)
	if err != nil {
		return err
	}
	_, err = ks.s.Exec(
		ctx,
		"create keypair "+k.Name,
		dialect.Insert("keypairs", strings.Split(keypairCols, ", ")),
		args...)
	return err
}

func (ks keys) Keypair(ctx context.Context, name string) (api.Keypair, error) {
	k, err := ks.scanKeypair(ks.s.Q(ctx).QueryRowContext(ctx,
		ks.s.Dialect().Rebind("SELECT "+keypairCols+" FROM keypairs WHERE name = ?"), name))
	return k, ks.s.Wrap(fmt.Sprintf("get keypair %q", name), err)
}

func (ks keys) Keypairs(ctx context.Context) ([]api.Keypair, error) {
	var out []api.Keypair
	err := ks.s.Rows(
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

func (ks keys) UpdateKeypair(ctx context.Context, k api.Keypair) error {
	args, err := ks.keypairArgs(k)
	if err != nil {
		return err
	}
	return ks.s.ExecOne(
		ctx,
		fmt.Sprintf("update keypair %q", k.Name),
		"UPDATE keypairs SET type = ?, alias = ?, public_key = ?, private_key = ?, passphrase = ? WHERE name = ?",
		append(args[1:], k.Name)...)
}

func (ks keys) DeleteKeypair(ctx context.Context, name string) error {
	return ks.s.ExecOne(
		ctx,
		fmt.Sprintf("delete keypair %q", name),
		"DELETE FROM keypairs WHERE name = ?",
		name,
	)
}

const certificateCols = "alias, pem, private_key, issued_to, issued_by, issued_on, valid_until, fingerprint"

func (ks keys) scanCertificate(row interface{ Scan(...any) error }) (api.Certificate, error) {
	var (
		c         api.Certificate
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
	c.IssuedOn, c.ValidUntil = dialect.FromMicros(on), dialect.FromMicros(until)
	var err error
	c.PrivateKey, err = ks.s.Unseal(purposeCertificateKey, c.Alias, key)
	return c, err
}

func (ks keys) PutCertificate(ctx context.Context, c api.Certificate) error {
	key, err := ks.s.Seal(purposeCertificateKey, c.Alias, c.PrivateKey)
	if err != nil {
		return err
	}
	_, err = ks.s.Exec(
		ctx,
		"put certificate "+c.Alias,
		dialect.Upsert("certificates", strings.Split(certificateCols, ", "), []string{"alias"}),
		c.Alias,
		c.PEM,
		key,
		c.IssuedTo,
		c.IssuedBy,
		dialect.Micros(c.IssuedOn),
		dialect.Micros(c.ValidUntil),
		c.Fingerprint,
	)
	return err
}

func (ks keys) Certificate(ctx context.Context, alias string) (api.Certificate, error) {
	c, err := ks.scanCertificate(ks.s.Q(ctx).QueryRowContext(ctx,
		ks.s.Dialect().Rebind("SELECT "+certificateCols+" FROM certificates WHERE alias = ?"),
		alias))
	return c, ks.s.Wrap(fmt.Sprintf("get certificate %q", alias), err)
}

func (ks keys) Certificates(ctx context.Context) ([]api.Certificate, error) {
	var out []api.Certificate
	err := ks.s.Rows(ctx, "list certificates",
		"SELECT "+certificateCols+" FROM certificates ORDER BY alias", nil,
		func(r *sql.Rows) error {
			c, err := ks.scanCertificate(r)
			out = append(out, c)
			return err
		})
	return out, err
}

func (ks keys) DeleteCertificate(ctx context.Context, alias string) error {
	return ks.s.ExecOne(ctx, fmt.Sprintf("delete certificate %q", alias),
		"DELETE FROM certificates WHERE alias = ?", alias)
}

const trustedKeyCols = "kid, alias, public_key, fingerprint, issued_on, issued_by, valid_until"

func scanTrustedKey(row interface{ Scan(...any) error }) (model.TrustedKey, error) {
	var (
		k         model.TrustedKey
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
	k.IssuedOn, k.ValidUntil = dialect.FromMicros(on), dialect.FromMicros(until)
	return k, nil
}

func (ks keys) CreateTrustedKey(ctx context.Context, k model.TrustedKey) error {
	_, err := ks.s.Exec(
		ctx,
		"create trusted key "+k.Kid,
		dialect.Insert("trusted_keys", strings.Split(trustedKeyCols, ", ")),
		k.Kid,
		dialect.NullString(k.Alias),
		k.PublicKey,
		k.Fingerprint,
		dialect.Micros(k.IssuedOn),
		k.IssuedBy,
		dialect.Micros(k.ValidUntil),
	)
	return err
}

func (ks keys) TrustedKey(ctx context.Context, kid string) (model.TrustedKey, error) {
	k, err := scanTrustedKey(ks.s.Q(ctx).QueryRowContext(ctx,
		ks.s.Dialect().Rebind("SELECT "+trustedKeyCols+" FROM trusted_keys WHERE kid = ?"), kid))
	return k, ks.s.Wrap(fmt.Sprintf("get trusted key %q", kid), err)
}

func (ks keys) TrustedKeys(ctx context.Context) ([]model.TrustedKey, error) {
	var out []model.TrustedKey
	err := ks.s.Rows(ctx, "list trusted keys",
		"SELECT "+trustedKeyCols+" FROM trusted_keys ORDER BY kid", nil,
		func(r *sql.Rows) error {
			k, err := scanTrustedKey(r)
			out = append(out, k)
			return err
		})
	return out, err
}

func (ks keys) DeleteTrustedKey(ctx context.Context, kid string) error {
	return ks.s.ExecOne(ctx, fmt.Sprintf("delete trusted key %q", kid),
		"DELETE FROM trusted_keys WHERE kid = ?", kid)
}

const signingKeyCols = "alias, public_key, private_key, passphrase, is_default"

func (ks keys) scanSigningKey(row interface{ Scan(...any) error }) (model.SigningKey, error) {
	var (
		k          model.SigningKey
		priv, pass []byte
	)
	if err := row.Scan(&k.Alias, &k.PublicKey, &priv, &pass, &k.IsDefault); err != nil {
		return k, err //nolint:wrapcheck // wrapped by the caller
	}
	var err error
	if k.PrivateKey, err = ks.s.Unseal(purposeSigningPrivate, k.Alias, priv); err != nil {
		return k, err
	}
	k.Passphrase, err = ks.s.Unseal(purposeSigningPassphrase, k.Alias, pass)
	return k, err
}

func (ks keys) PutSigningKey(ctx context.Context, k model.SigningKey) error {
	priv, err := ks.s.Seal(purposeSigningPrivate, k.Alias, k.PrivateKey)
	if err != nil {
		return err
	}
	pass, err := ks.s.Seal(purposeSigningPassphrase, k.Alias, k.Passphrase)
	if err != nil {
		return err
	}
	return ks.s.Tx(ctx, func(ctx context.Context) error {
		if k.IsDefault {
			if _, err := ks.s.Exec(ctx, "clear default signing key",
				"UPDATE signing_keys SET is_default = ? WHERE alias <> ? AND is_default = ?",
				false, k.Alias, true); err != nil {
				return err
			}
		}
		_, err := ks.s.Exec(ctx, "put signing key "+k.Alias,
			dialect.Upsert("signing_keys", strings.Split(signingKeyCols, ", "), []string{"alias"}),
			k.Alias, k.PublicKey, priv, pass, k.IsDefault)
		return err
	})
}

func (ks keys) signingKey(
	ctx context.Context,
	op, where string,
	arg any,
) (model.SigningKey, error) {
	k, err := ks.scanSigningKey(ks.s.Q(ctx).QueryRowContext(ctx,
		ks.s.Dialect().
			Rebind("SELECT "+signingKeyCols+" FROM signing_keys WHERE "+where+" ORDER BY alias LIMIT 1"),
		arg),
	)
	return k, ks.s.Wrap(op, err)
}

func (ks keys) SigningKey(ctx context.Context, alias string) (model.SigningKey, error) {
	return ks.signingKey(ctx, fmt.Sprintf("get signing key %q", alias), "alias = ?", alias)
}

func (ks keys) DefaultSigningKey(ctx context.Context) (model.SigningKey, error) {
	return ks.signingKey(ctx, "get default signing key", "is_default = ?", true)
}

func (ks keys) SigningKeys(ctx context.Context) ([]model.SigningKey, error) {
	var out []model.SigningKey
	err := ks.s.Rows(ctx, "list signing keys",
		"SELECT "+signingKeyCols+" FROM signing_keys ORDER BY alias", nil,
		func(r *sql.Rows) error {
			k, err := ks.scanSigningKey(r)
			out = append(out, k)
			return err
		})
	return out, err
}

func (ks keys) DeleteSigningKey(ctx context.Context, alias string) error {
	return ks.s.ExecOne(ctx, fmt.Sprintf("delete signing key %q", alias),
		"DELETE FROM signing_keys WHERE alias = ?", alias)
}
