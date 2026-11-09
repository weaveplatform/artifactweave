package inmem

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Tokens implements store.Store.
func (s *Store) Tokens() store.TokenStore { return tokens{s} }

// Keys implements store.Store.
func (s *Store) Keys() store.KeyStore { return keys{s} }

// --- Tokens ----------------------------------------------------------------

type tokens struct{ s *Store }

func (ts tokens) t() *memtx.Table[string, types.Token] {
	return memtx.TableOf[string, types.Token](ts.s.Store, "tokens")
}

// issuedKey is the token list's first sort key: Unix microseconds, 0 for an
// unset time, as the SQL column stores it.
func issuedKey(t types.Token) int64 {
	if t.IssuedAt.IsZero() {
		return 0
	}
	return t.IssuedAt.UnixMicro()
}

func compareTokens(a, b types.Token) int {
	if c := issuedKey(a) - issuedKey(b); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	return strings.Compare(a.ID, b.ID)
}

// digestOf returns a token's digest of one kind.
func digestOf(t types.Token, kind store.DigestKind) []byte {
	switch kind {
	case store.DigestAccess:
		return t.AccessDigest
	case store.DigestRefresh:
		return t.RefreshDigest
	default:
		return t.ReferenceDigest
	}
}

var digestKinds = []store.DigestKind{store.DigestAccess, store.DigestRefresh, store.DigestReference}

func (ts tokens) Create(ctx context.Context, t types.Token) error {
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

func (ts tokens) Get(ctx context.Context, id string) (types.Token, error) {
	var out types.Token
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
	kind store.DigestKind,
	d []byte,
) (types.Token, error) {
	var out types.Token
	err := ts.s.Do(ctx, func() error {
		if kind < store.DigestAccess || kind > store.DigestReference {
			return fmt.Errorf("token digest kind %d: %w", kind, store.ErrInvalid)
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
	f store.TokenFilter,
	p store.Page,
) ([]types.Token, string, error) {
	var (
		out  []types.Token
		next string
	)
	err := ts.s.Do(ctx, func() error {
		rows := ts.t().All(func(_ string, t types.Token) bool {
			return (f.Subject == "" || t.Subject == f.Subject) &&
				(f.Purpose == "" || t.Purpose == f.Purpose)
		}, compareTokens)
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(t types.Token) []any { return []any{issuedKey(t), t.ID} },
		)
		return err
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
		n = ts.t().DeleteWhere(func(_ string, t types.Token) bool {
			return !t.ExpiresAt.IsZero() && t.ExpiresAt.UnixMicro() < cut
		})
		return nil
	})
	return n, err
}

// --- Keys ------------------------------------------------------------------

type keys struct{ s *Store }

func (ks keys) keypairs() *memtx.Table[string, types.Keypair] {
	return memtx.TableOf[string, types.Keypair](ks.s.Store, "keypairs")
}

func (ks keys) certificates() *memtx.Table[string, types.Certificate] {
	return memtx.TableOf[string, types.Certificate](ks.s.Store, "certificates")
}

func (ks keys) trusted() *memtx.Table[string, types.TrustedKey] {
	return memtx.TableOf[string, types.TrustedKey](ks.s.Store, "trusted_keys")
}

func (ks keys) signing() *memtx.Table[string, types.SigningKey] {
	return memtx.TableOf[string, types.SigningKey](ks.s.Store, "signing_keys")
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

func (ks keys) CreateKeypair(ctx context.Context, k types.Keypair) error {
	return ks.s.Do(ctx, func() error {
		if ks.keypairs().Has(k.Name) {
			return memtx.Conflict("keypair", k.Name)
		}
		ks.keypairs().Put(k.Name, k)
		return nil
	})
}

func (ks keys) Keypair(ctx context.Context, name string) (types.Keypair, error) {
	return getKeyed(ks.s, ctx, ks.keypairs, "keypair", name)
}

func (ks keys) Keypairs(ctx context.Context) ([]types.Keypair, error) {
	return listKeyed(ks.s, ctx, ks.keypairs, func(k types.Keypair) string { return k.Name })
}

func (ks keys) UpdateKeypair(ctx context.Context, k types.Keypair) error {
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

func (ks keys) PutCertificate(ctx context.Context, c types.Certificate) error {
	return ks.s.Do(ctx, func() error {
		ks.certificates().Put(c.Alias, c)
		return nil
	})
}

func (ks keys) Certificate(ctx context.Context, alias string) (types.Certificate, error) {
	return getKeyed(ks.s, ctx, ks.certificates, "certificate", alias)
}

func (ks keys) Certificates(ctx context.Context) ([]types.Certificate, error) {
	return listKeyed(
		ks.s,
		ctx,
		ks.certificates,
		func(c types.Certificate) string { return c.Alias },
	)
}

func (ks keys) DeleteCertificate(ctx context.Context, alias string) error {
	return deleteKeyed(ks.s, ctx, ks.certificates, "certificate", alias)
}

func (ks keys) CreateTrustedKey(ctx context.Context, k types.TrustedKey) error {
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

func (ks keys) TrustedKey(ctx context.Context, kid string) (types.TrustedKey, error) {
	return getKeyed(ks.s, ctx, ks.trusted, "trusted key", kid)
}

func (ks keys) TrustedKeys(ctx context.Context) ([]types.TrustedKey, error) {
	return listKeyed(ks.s, ctx, ks.trusted, func(k types.TrustedKey) string { return k.Kid })
}

func (ks keys) DeleteTrustedKey(ctx context.Context, kid string) error {
	return deleteKeyed(ks.s, ctx, ks.trusted, "trusted key", kid)
}

func (ks keys) PutSigningKey(ctx context.Context, k types.SigningKey) error {
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

func (ks keys) SigningKey(ctx context.Context, alias string) (types.SigningKey, error) {
	return getKeyed(ks.s, ctx, ks.signing, "signing key", alias)
}

func (ks keys) SigningKeys(ctx context.Context) ([]types.SigningKey, error) {
	return listKeyed(ks.s, ctx, ks.signing, func(k types.SigningKey) string { return k.Alias })
}

func (ks keys) DefaultSigningKey(ctx context.Context) (types.SigningKey, error) {
	var out types.SigningKey
	err := ks.s.Do(ctx, func() error {
		// PutSigningKey keeps at most one default; the lowest alias wins
		// anyway, as the SQL query orders, should two ever exist.
		found := false
		for alias, k := range ks.signing().Rows {
			if k.IsDefault && (!found || alias < out.Alias) {
				out, found = store.Clone(k), true
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
