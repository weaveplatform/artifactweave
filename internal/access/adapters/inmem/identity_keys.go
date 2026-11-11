package inmem

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Tokens returns the token store: token records (decision 0005). Nothing
// here outlives the process, so a purposed token's secret is kept as
// given, not sealed as the SQL store seals it.
func (s *Store) Tokens() port.TokenStore { return tokens{s} }

// Keys returns the key store: key pairs, certificates, trusted keys and
// signing keys. Nothing here outlives the process, so private keys and
// passphrases are kept as given, not sealed as the SQL store seals them.
func (s *Store) Keys() port.KeyStore { return keys{s} }

// --- Tokens ----------------------------------------------------------------

type tokens struct{ s *Store }

func (ts tokens) t() *memtx.Table[string, model.Token] {
	return memtx.TableOf[string, model.Token](ts.s.Store, "tokens")
}

// issuedKey is the token list's first sort key: Unix microseconds, 0 for an
// unset time, as the SQL column stores it.
func issuedKey(t model.Token) int64 {
	if t.IssuedAt.IsZero() {
		return 0
	}
	return t.IssuedAt.UnixMicro()
}

func compareTokens(a, b model.Token) int {
	if c := issuedKey(a) - issuedKey(b); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	return strings.Compare(a.ID, b.ID)
}

// digestOf returns a token's digest of one kind.
func digestOf(t model.Token, kind port.DigestKind) []byte {
	switch kind {
	case port.DigestAccess:
		return t.AccessDigest
	case port.DigestRefresh:
		return t.RefreshDigest
	default:
		return t.ReferenceDigest
	}
}

var digestKinds = []port.DigestKind{port.DigestAccess, port.DigestRefresh, port.DigestReference}

func (ts tokens) Create(ctx context.Context, t model.Token) error {
	return ts.s.Do(ctx, func() error {
		if ts.t().Has(t.ID) {
			return memtx.Conflict("token", t.ID)
		}
		for _, other := range ts.t().Rows {
			if t.Purpose != "" && other.Purpose == t.Purpose && other.Subject == t.Subject {
				return memtx.Conflict("token of purpose "+t.Purpose+" for", t.Subject)
			}
			for _, k := range digestKinds {
				if d := digestOf(t, k); len(d) > 0 && bytes.Equal(d, digestOf(other, k)) {
					return memtx.Conflict("token digest of", t.ID)
				}
			}
		}
		ts.t().Put(t.ID, t)
		return nil
	})
}

func (ts tokens) Get(ctx context.Context, id string) (model.Token, error) {
	var out model.Token
	err := ts.s.Do(ctx, func() error {
		var ok bool
		if out, ok = ts.t().Get(id); !ok {
			return memtx.NotFound("token", id)
		}
		return nil
	})
	return out, err
}

func (ts tokens) ByDigest(
	ctx context.Context,
	kind port.DigestKind,
	d []byte,
) (model.Token, error) {
	var out model.Token
	err := ts.s.Do(ctx, func() error {
		if kind < port.DigestAccess || kind > port.DigestReference {
			return fmt.Errorf("token digest kind %d: %w", kind, kernel.ErrInvalid)
		}
		if len(d) > 0 {
			for id, t := range ts.t().Rows {
				if bytes.Equal(digestOf(t, kind), d) {
					out, _ = ts.t().Get(id)
					return nil
				}
			}
		}
		return memtx.NotFound("token by digest kind", kind)
	})
	return out, err
}

func (ts tokens) List(
	ctx context.Context,
	f port.TokenFilter,
	p kernel.Page,
) ([]model.Token, string, error) {
	var (
		out  []model.Token
		next string
	)
	err := ts.s.Do(ctx, func() error {
		rows := ts.t().All(func(_ string, t model.Token) bool {
			return (f.Subject == "" || t.Subject == f.Subject) &&
				(f.Purpose == "" || t.Purpose == f.Purpose)
		}, compareTokens)
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(t model.Token) []any { return []any{issuedKey(t), t.ID} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (ts tokens) Delete(ctx context.Context, id string) error {
	return ts.s.Do(ctx, func() error {
		if !ts.t().Del(id) {
			return memtx.NotFound("token", id)
		}
		return nil
	})
}

func (ts tokens) Touch(ctx context.Context, id string, at time.Time) error {
	return ts.s.Do(ctx, func() error {
		t, ok := ts.t().Get(id)
		if !ok {
			return memtx.NotFound("token", id)
		}
		t.LastUsed = at
		ts.t().Put(id, t)
		return nil
	})
}

func (ts tokens) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := ts.s.Do(ctx, func() error {
		cut := before.UnixMicro()
		n = ts.t().DeleteWhere(func(_ string, t model.Token) bool {
			return !t.ExpiresAt.IsZero() && t.ExpiresAt.UnixMicro() < cut
		})
		return nil
	})
	return n, err
}

// --- Keys ------------------------------------------------------------------

type keys struct{ s *Store }

func (ks keys) keypairs() *memtx.Table[string, api.Keypair] {
	return memtx.TableOf[string, api.Keypair](ks.s.Store, "keypairs")
}

func (ks keys) certificates() *memtx.Table[string, api.Certificate] {
	return memtx.TableOf[string, api.Certificate](ks.s.Store, "certificates")
}

func (ks keys) trusted() *memtx.Table[string, model.TrustedKey] {
	return memtx.TableOf[string, model.TrustedKey](ks.s.Store, "trusted_keys")
}

func (ks keys) signing() *memtx.Table[string, model.SigningKey] {
	return memtx.TableOf[string, model.SigningKey](ks.s.Store, "signing_keys")
}

// getKeyed returns the row at key or ErrNotFound naming what.
func getKeyed[V any](
	s *Store,
	ctx context.Context,
	t func() *memtx.Table[string, V],
	what, key string,
) (V, error) {
	var out V
	err := s.Do(ctx, func() error {
		var ok bool
		if out, ok = t().Get(key); !ok {
			return memtx.NotFound(what, key)
		}
		return nil
	})
	return out, err
}

// deleteKeyed removes the row at key or returns ErrNotFound naming what.
func deleteKeyed[V any](
	s *Store,
	ctx context.Context,
	t func() *memtx.Table[string, V],
	what, key string,
) error {
	return s.Do(ctx, func() error {
		if !t().Del(key) {
			return memtx.NotFound(what, key)
		}
		return nil
	})
}

// listKeyed lists every row ordered by key.
func listKeyed[V any](
	s *Store,
	ctx context.Context,
	t func() *memtx.Table[string, V],
	key func(V) string,
) ([]V, error) {
	var out []V
	err := s.Do(ctx, func() error {
		out = t().All(nil, compareByKey(key))
		return nil
	})
	return out, err
}

func (ks keys) CreateKeypair(ctx context.Context, k api.Keypair) error {
	return ks.s.Do(ctx, func() error {
		if ks.keypairs().Has(k.Name) {
			return memtx.Conflict("keypair", k.Name)
		}
		ks.keypairs().Put(k.Name, k)
		return nil
	})
}

func (ks keys) Keypair(ctx context.Context, name string) (api.Keypair, error) {
	return getKeyed(ks.s, ctx, ks.keypairs, "keypair", name)
}

func (ks keys) Keypairs(ctx context.Context) ([]api.Keypair, error) {
	return listKeyed(ks.s, ctx, ks.keypairs, func(k api.Keypair) string { return k.Name })
}

func (ks keys) UpdateKeypair(ctx context.Context, k api.Keypair) error {
	return ks.s.Do(ctx, func() error {
		if !ks.keypairs().Has(k.Name) {
			return memtx.NotFound("keypair", k.Name)
		}
		ks.keypairs().Put(k.Name, k)
		return nil
	})
}

func (ks keys) DeleteKeypair(ctx context.Context, name string) error {
	return deleteKeyed(ks.s, ctx, ks.keypairs, "keypair", name)
}

func (ks keys) PutCertificate(ctx context.Context, c api.Certificate) error {
	return ks.s.Do(ctx, func() error {
		ks.certificates().Put(c.Alias, c)
		return nil
	})
}

func (ks keys) Certificate(ctx context.Context, alias string) (api.Certificate, error) {
	return getKeyed(ks.s, ctx, ks.certificates, "certificate", alias)
}

func (ks keys) Certificates(ctx context.Context) ([]api.Certificate, error) {
	return listKeyed(
		ks.s,
		ctx,
		ks.certificates,
		func(c api.Certificate) string { return c.Alias },
	)
}

func (ks keys) DeleteCertificate(ctx context.Context, alias string) error {
	return deleteKeyed(ks.s, ctx, ks.certificates, "certificate", alias)
}

func (ks keys) CreateTrustedKey(ctx context.Context, k model.TrustedKey) error {
	return ks.s.Do(ctx, func() error {
		if ks.trusted().Has(k.Kid) {
			return memtx.Conflict("trusted key", k.Kid)
		}
		for _, other := range ks.trusted().Rows {
			if k.Alias != "" && other.Alias == k.Alias {
				return memtx.Conflict("trusted key alias", k.Alias)
			}
		}
		ks.trusted().Put(k.Kid, k)
		return nil
	})
}

func (ks keys) TrustedKey(ctx context.Context, kid string) (model.TrustedKey, error) {
	return getKeyed(ks.s, ctx, ks.trusted, "trusted key", kid)
}

func (ks keys) TrustedKeys(ctx context.Context) ([]model.TrustedKey, error) {
	return listKeyed(ks.s, ctx, ks.trusted, func(k model.TrustedKey) string { return k.Kid })
}

func (ks keys) DeleteTrustedKey(ctx context.Context, kid string) error {
	return deleteKeyed(ks.s, ctx, ks.trusted, "trusted key", kid)
}

func (ks keys) PutSigningKey(ctx context.Context, k model.SigningKey) error {
	return ks.s.Do(ctx, func() error {
		if k.IsDefault {
			for alias, other := range ks.signing().Rows {
				if alias != k.Alias && other.IsDefault {
					other.IsDefault = false
					ks.signing().Put(alias, other)
				}
			}
		}
		ks.signing().Put(k.Alias, k)
		return nil
	})
}

func (ks keys) SigningKey(ctx context.Context, alias string) (model.SigningKey, error) {
	return getKeyed(ks.s, ctx, ks.signing, "signing key", alias)
}

func (ks keys) SigningKeys(ctx context.Context) ([]model.SigningKey, error) {
	return listKeyed(ks.s, ctx, ks.signing, func(k model.SigningKey) string { return k.Alias })
}

func (ks keys) DefaultSigningKey(ctx context.Context) (model.SigningKey, error) {
	var out model.SigningKey
	err := ks.s.Do(ctx, func() error {
		// PutSigningKey keeps at most one default; the lowest alias wins
		// anyway, as the SQL query orders, should two ever exist.
		found := false
		for alias, k := range ks.signing().Rows {
			if k.IsDefault && (!found || alias < out.Alias) {
				out, found = kernel.Clone(k), true
			}
		}
		if !found {
			return memtx.NotFound("signing key", "default")
		}
		return nil
	})
	return out, err
}

func (ks keys) DeleteSigningKey(ctx context.Context, alias string) error {
	return deleteKeyed(ks.s, ctx, ks.signing, "signing key", alias)
}
