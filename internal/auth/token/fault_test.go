package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// faultStore fails the named operations of an in-memory store.
type faultStore struct {
	*inmem.Store
	fail map[string]bool
}

func (s *faultStore) Settings() store.SettingsStore {
	return faultSettings{s.Store.Settings(), s.fail}
}
func (s *faultStore) Keys() store.KeyStore     { return faultKeys{s.Store.Keys(), s.fail} }
func (s *faultStore) Tokens() store.TokenStore { return faultTokens{s.Store.Tokens(), s.fail} }

type faultSettings struct {
	store.SettingsStore
	fail map[string]bool
}

func (x faultSettings) Get(ctx context.Context, k string, into any) error {
	if x.fail["settings.get"] {
		return errBoom
	}
	return x.SettingsStore.Get(ctx, k, into) //nolint:wrapcheck // test fake
}

func (x faultSettings) Put(ctx context.Context, k string, v any) error {
	if x.fail["settings.put"] {
		return errBoom
	}
	return x.SettingsStore.Put(ctx, k, v) //nolint:wrapcheck // test fake
}

type faultKeys struct {
	store.KeyStore
	fail map[string]bool
}

func (x faultKeys) SigningKeys(ctx context.Context) ([]types.SigningKey, error) {
	if x.fail["keys.list"] {
		return nil, errBoom
	}
	return x.KeyStore.SigningKeys(ctx) //nolint:wrapcheck // test fake
}

func (x faultKeys) PutSigningKey(ctx context.Context, k types.SigningKey) error {
	if x.fail["keys.put"] {
		return errBoom
	}
	return x.KeyStore.PutSigningKey(ctx, k) //nolint:wrapcheck // test fake
}

type faultTokens struct {
	store.TokenStore
	fail map[string]bool
}

func (x faultTokens) Create(ctx context.Context, t types.Token) error {
	if x.fail["tokens.create"] {
		return errBoom
	}
	return x.TokenStore.Create(ctx, t) //nolint:wrapcheck // test fake
}

func (x faultTokens) Get(ctx context.Context, id string) (types.Token, error) {
	if x.fail["tokens.get"] {
		return types.Token{}, errBoom
	}
	return x.TokenStore.Get(ctx, id) //nolint:wrapcheck // test fake
}

func (x faultTokens) ByDigest(
	ctx context.Context,
	k store.DigestKind,
	d []byte,
) (types.Token, error) {
	if x.fail["tokens.bydigest"] {
		return types.Token{}, errBoom
	}
	return x.TokenStore.ByDigest(ctx, k, d) //nolint:wrapcheck // test fake
}

func (x faultTokens) List(
	ctx context.Context,
	f store.TokenFilter,
	p store.Page,
) ([]types.Token, string, error) {
	if x.fail["tokens.list"] {
		return nil, "", errBoom
	}
	return x.TokenStore.List(ctx, f, p) //nolint:wrapcheck // test fake
}

func (x faultTokens) Delete(ctx context.Context, id string) error {
	if x.fail["tokens.delete"] {
		return errBoom
	}
	return x.TokenStore.Delete(ctx, id) //nolint:wrapcheck // test fake
}

func faulty(t *testing.T) (*Service, *faultStore, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	fs := &faultStore{Store: newStore(t), fail: map[string]bool{}}
	return newService(t, fs, c), fs, c
}

func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	s, fs, c := faulty(t)
	out := issue(t, s, Request{ForceRevocable: true, IncludeReference: true, Refreshable: true})
	check := func(op string, run func() error) {
		t.Helper()
		fs.fail = map[string]bool{op: true}
		err := run()
		fs.fail = map[string]bool{}
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", op, err)
		}
	}
	check(
		"settings.get",
		func() error { _, err := s.Issue(ctx, Request{Username: "a"}); return err },
	)
	check(
		"tokens.create",
		func() error { _, err := s.Issue(ctx, Request{Username: "a"}); return err },
	)
	check("tokens.get", func() error { _, err := s.Verify(ctx, out.AccessToken); return err })
	check(
		"tokens.bydigest",
		func() error { _, err := s.Verify(ctx, out.ReferenceToken); return err },
	)
	check("tokens.delete", func() error { return s.Revoke(ctx, out.Record.ID) })
	check("tokens.delete", func() error { _, err := s.Refresh(ctx, out.RefreshToken); return err })
	check("tokens.list", func() error { _, err := s.EncryptedPassword(ctx, "a"); return err })
	check("tokens.create", func() error { _, err := s.EncryptedPassword(ctx, "a"); return err })
	check("tokens.list", func() error { return s.Rotate(ctx) })
	check("settings.get", func() error { return s.Rotate(ctx) })
	check("keys.put", func() error { return s.Rotate(ctx) })
	check("settings.put", func() error { return s.Rotate(ctx) })
	// An unknown key reloads, and a failed reload is not "invalid".
	unknown := b64.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"nope"}`))
	check("keys.list", func() error { _, err := s.Verify(ctx, unknown+".e30.eHh4"); return err })

	for _, op := range []string{"settings.get", "keys.list"} {
		fs2 := &faultStore{Store: newStore(t), fail: map[string]bool{op: true}}
		if _, err := New(
			ctx,
			fs2,
			Options{Now: c.now, KeyBits: testBits},
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("New with %s failing: %v", op, err)
		}
	}
	fs3 := &faultStore{Store: newStore(t), fail: map[string]bool{"keys.put": true}}
	if _, err := New(ctx, fs3, Options{KeyBits: testBits}); !errors.Is(err, errBoom) {
		t.Errorf("New without a key and failing to store one: %v", err)
	}
}

func pemOf(t *testing.T, typ string, der []byte, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

// TestBadStoredKeys: a key that does not parse, or is not RSA, stops the
// service rather than being skipped, because a silently ignored key would
// refuse the tokens it signed.
func TestBadStoredKeys(t *testing.T) {
	ctx := context.Background()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rk, err := rsa.GenerateKey(rand.Reader, testBits)
	if err != nil {
		t.Fatal(err)
	}
	ecPriv := pemOf(t, "PRIVATE KEY", nil, nil)
	if der, err := x509.MarshalPKCS8PrivateKey(ec); err == nil {
		ecPriv = pemOf(t, "PRIVATE KEY", der, nil)
	}
	ecPubDER, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	ecPub := pemOf(t, "PUBLIC KEY", ecPubDER, err)
	rsaPubDER, err := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	rsaPub := pemOf(t, "PUBLIC KEY", rsaPubDER, err)
	garbage := pemOf(t, "PUBLIC KEY", []byte("garbage"), nil)
	for name, k := range map[string]types.SigningKey{
		"no pem public":   {PublicKey: "not pem", PrivateKey: ecPriv},
		"garbage public":  {PublicKey: garbage, PrivateKey: ecPriv},
		"ec public":       {PublicKey: ecPub, PrivateKey: ecPriv},
		"no pem private":  {PublicKey: rsaPub, PrivateKey: "not pem"},
		"garbage private": {PublicKey: rsaPub, PrivateKey: garbage},
		"ec private":      {PublicKey: rsaPub, PrivateKey: ecPriv},
	} {
		db := newStore(t)
		k.Alias = keyPrefix + "ffffffffffffffff00000000"
		if err := db.Keys().PutSigningKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		if _, err := New(ctx, db, Options{KeyBits: testBits}); !errors.Is(err, errBadKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestRecordMismatches: a JWT whose record names another subject, and a
// refresh record whose subject is not an Access subject, are invalid.
func TestRecordMismatches(t *testing.T) {
	ctx := context.Background()
	s, db, _ := world(t)
	out := issue(t, s, Request{})
	rec, err := db.Tokens().Get(ctx, out.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Tokens().Delete(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	rec.Subject = s.Issuer() + "/users/mallory"
	if err := db.Tokens().Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	invalid(t, s, out.AccessToken, "record of another subject")
	if err := db.Tokens().Create(ctx, types.Token{
		ID: "bad", Subject: "nobody", Refreshable: true, RefreshDigest: digest("r"),
		IssuedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, "r"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("refresh of a bad subject: %v", err)
	}
	if err := db.Settings().Put(ctx, types.SettingDefaultTokenExpiry, "soon"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issue(ctx, Request{Username: "a"}); err == nil {
		t.Fatal("a default expiry that is not a number")
	}
}
