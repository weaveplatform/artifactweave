package token

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const testBits = 1024

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newStore(t *testing.T) *inmem.Store {
	t.Helper()
	db := inmem.New()
	if err := db.Instance().Init(context.Background(), types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func newService(t *testing.T, db store.Store, c *clock) *Service {
	t.Helper()
	s, err := New(context.Background(), db, Options{Now: c.now, KeyBits: testBits})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func world(t *testing.T) (*Service, *inmem.Store, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	db := newStore(t)
	return newService(t, db, c), db, c
}

func issue(t *testing.T, s *Service, r Request) Issued {
	t.Helper()
	if r.Username == "" {
		r.Username = "alice"
	}
	out, err := s.Issue(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dur(d time.Duration) *time.Duration { return &d }

func invalid(t *testing.T, s *Service, raw, why string) {
	t.Helper()
	if _, err := s.Verify(context.Background(), raw); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("%s: verify = %v, want ErrInvalidToken", why, err)
	}
}

// TestCLIDecodesThePayload is the JFrog CLI's own reading of a token: the
// payload through base64.RawStdEncoding, then sub's "/users/<name>".
func TestCLIDecodesThePayload(t *testing.T) {
	s, _, _ := world(t)
	for _, name := range []string{"alice", "b?b", "c>d~e", "zoë", "x😀y"} {
		out := issue(t, s, Request{Username: name, Description: "what? <ok> ~tilde~ ünïcode 😀"})
		payload := strings.Split(out.AccessToken, ".")[1]
		raw, err := base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			t.Fatalf("%s: the CLI cannot decode %q: %v", name, payload, err)
		}
		var c struct{ Sub string }
		if err := json.Unmarshal(raw, &c); err != nil || !strings.HasSuffix(c.Sub, "/users/"+name) {
			t.Fatalf("%s: sub %q, %v", name, c.Sub, err)
		}
	}
}

// TestCLISafeProperty checks the encoding argument on random strings: the
// payload stays the same JSON and its base64url has no '-' or '_'.
func TestCLISafeProperty(t *testing.T) {
	alphabet := []rune("ab?>~\x7f\"\\/é😀  {}")
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		var b strings.Builder
		for range r.IntN(40) {
			b.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		in := map[string]string{"sub": b.String(), "scp": "applied-permissions/user"}
		j, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		safe := cliSafe(j)
		enc := b64.EncodeToString(safe)
		if strings.ContainsAny(enc, "-_") {
			t.Fatalf("%q encodes to %q", safe, enc)
		}
		var out map[string]string
		if err := json.Unmarshal(safe, &out); err != nil || out["sub"] != in["sub"] {
			t.Fatalf("%q round trip: %q, %v", b.String(), out["sub"], err)
		}
		if std, err := base64.RawStdEncoding.DecodeString(
			enc,
		); err != nil ||
			!bytes.Equal(std, safe) {
			t.Fatalf("standard decoding differs: %v", err)
		}
	}
}

func TestIssueAndVerify(t *testing.T) {
	s, db, _ := world(t)
	ctx := context.Background()
	out := issue(t, s, Request{IncludeReference: true, Description: "ci"})
	if out.ExpiresIn != 3600 || out.Record.Audience != DefaultAudience ||
		out.Record.Subject != "jfac@01234567-89ab-4cde-8f01-23456789abcd/users/alice" ||
		out.Record.Revocable || out.RefreshToken != "" || !types.IsReferenceToken(out.ReferenceToken) {
		t.Fatalf("issued %+v", out)
	}
	if s.Issuer() != "jfac@01234567-89ab-4cde-8f01-23456789abcd" {
		t.Fatalf("issuer %s", s.Issuer())
	}
	for _, raw := range []string{out.AccessToken, out.ReferenceToken} {
		rec, err := s.Verify(ctx, raw)
		if err != nil || rec.ID != out.Record.ID ||
			rec.Scope.String() != "applied-permissions/user" {
			t.Fatalf("verify %q: %+v, %v", raw[:10], rec, err)
		}
	}
	// The default lifetime is the setting when one is stored.
	if err := db.Settings().Put(ctx, types.SettingDefaultTokenExpiry, 120); err != nil {
		t.Fatal(err)
	}
	if out := issue(t, s, Request{}); out.ExpiresIn != 120 {
		t.Fatalf("default expiry %d", out.ExpiresIn)
	}
	if err := db.Settings().Put(ctx, types.SettingDefaultTokenExpiry, -1); err != nil {
		t.Fatal(err)
	}
	if out := issue(t, s, Request{}); out.ExpiresIn != 3600 {
		t.Fatalf("a negative setting falls back: %d", out.ExpiresIn)
	}
	// A long-lived token is revocable; a scope is kept as given.
	sc, err := types.ParseScope("applied-permissions/groups:readers")
	if err != nil {
		t.Fatal(err)
	}
	out = issue(t, s, Request{ExpiresIn: dur(7 * time.Hour), Scope: sc, Refreshable: true})
	if !out.Record.Revocable || out.RefreshToken == "" || out.Record.Scope.String() != sc.String() {
		t.Fatalf("issued %+v", out)
	}
}

func TestIssueRefuses(t *testing.T) {
	s, _, _ := world(t)
	ctx := context.Background()
	for name, r := range map[string]Request{
		"no user":         {},
		"slash in user":   {Username: "a/b"},
		"negative":        {Username: "a", ExpiresIn: dur(-time.Second)},
		"never, no admin": {Username: "a", ExpiresIn: dur(0)},
	} {
		if _, err := s.Issue(ctx, r); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	out := issue(t, s, Request{ExpiresIn: dur(0), ByAdmin: true})
	if out.ExpiresIn != 0 || !out.Record.ExpiresAt.IsZero() || !out.Record.Revocable {
		t.Fatalf("never expiring %+v", out)
	}
	if strings.Contains(string(decodePayload(t, out.AccessToken)), `"exp"`) {
		t.Fatal("a token that never expires carries exp")
	}
}

func decodePayload(t *testing.T, raw string) []byte {
	t.Helper()
	b, err := b64.DecodeString(strings.Split(raw, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTamperedTokens(t *testing.T) {
	s, _, _ := world(t)
	good := issue(t, s, Request{}).AccessToken
	parts := strings.Split(good, ".")
	other := strings.Replace(string(decodePayload(t, good)), "/users/alice", "/users/admin", 1)
	sig := []byte(parts[2])
	sig[5] ^= 1
	none := b64.EncodeToString([]byte(`{"alg":"none","typ":"JWT","kid":"x"}`))
	hs := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	unknown := b64.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"nope"}`))
	for why, raw := range map[string]string{
		"payload changed":   parts[0] + "." + b64.EncodeToString([]byte(other)) + "." + parts[2],
		"signature changed": parts[0] + "." + parts[1] + "." + string(sig),
		"alg none":          none + "." + parts[1] + ".",
		"alg HS256":         hs + "." + parts[1] + "." + parts[2],
		"unknown key":       unknown + "." + parts[1] + "." + parts[2],
		"two parts":         parts[0] + "." + parts[1],
		"bad header":        "!!." + parts[1] + "." + parts[2],
		"header not json":   b64.EncodeToString([]byte("x")) + "." + parts[1] + "." + parts[2],
		"bad payload":       parts[0] + ".!!." + parts[2],
		"bad signature":     parts[0] + "." + parts[1] + ".!!",
		"unknown reference": newReference(),
	} {
		invalid(t, s, raw, why)
	}
}

func TestExpiryAndTouch(t *testing.T) {
	s, db, c := world(t)
	ctx := context.Background()
	out := issue(t, s, Request{IncludeReference: true, ExpiresIn: dur(time.Hour)})
	if _, err := s.Verify(ctx, out.AccessToken); err != nil {
		t.Fatal(err)
	}
	rec, err := db.Tokens().Get(ctx, out.Record.ID)
	if err != nil || !rec.LastUsed.Equal(c.now()) {
		t.Fatalf("last used %v, %v", rec.LastUsed, err)
	}
	// Within a minute no second write.
	c.add(30 * time.Second)
	if _, err := s.Verify(ctx, out.AccessToken); err != nil {
		t.Fatal(err)
	}
	if rec, _ := db.Tokens().Get(ctx, out.Record.ID); rec.LastUsed.Equal(c.now()) {
		t.Fatal("touched again within a minute")
	}
	c.add(time.Hour)
	invalid(t, s, out.AccessToken, "expired JWT")
	invalid(t, s, out.ReferenceToken, "expired reference")
}

func TestRevoke(t *testing.T) {
	s, _, _ := world(t)
	ctx := context.Background()
	long := issue(t, s, Request{ForceRevocable: true, IncludeReference: true})
	if err := s.Revoke(ctx, long.Record.ID); err != nil {
		t.Fatal(err)
	}
	invalid(t, s, long.AccessToken, "revoked by id")
	invalid(t, s, long.ReferenceToken, "reference of a revoked token")
	byValue := issue(t, s, Request{ForceRevocable: true})
	if err := s.RevokeValue(ctx, byValue.AccessToken); err != nil {
		t.Fatal(err)
	}
	invalid(t, s, byValue.AccessToken, "revoked by value")
	byRef := issue(t, s, Request{ForceRevocable: true, IncludeReference: true})
	if err := s.RevokeValue(ctx, byRef.ReferenceToken); err != nil {
		t.Fatal(err)
	}
	invalid(t, s, byRef.AccessToken, "revoked by reference")
	short := issue(t, s, Request{})
	if err := s.Revoke(ctx, short.Record.ID); !errors.Is(err, ErrNotRevocable) {
		t.Fatalf("short-lived token revoked: %v", err)
	}
	if err := s.Revoke(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke missing: %v", err)
	}
	if err := s.RevokeValue(ctx, "eyJ.x.y"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke unknown value: %v", err)
	}
}

func TestRefresh(t *testing.T) {
	s, _, c := world(t)
	ctx := context.Background()
	first := issue(
		t,
		s,
		Request{Refreshable: true, IncludeReference: true, ExpiresIn: dur(2 * time.Hour)},
	)
	c.add(3 * time.Hour) // expired, within the refresh grace
	next, err := s.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.ExpiresIn != 7200 || next.RefreshToken == "" || next.ReferenceToken == "" ||
		next.Record.Subject != first.Record.Subject {
		t.Fatalf("refreshed %+v", next)
	}
	if _, err := s.Verify(ctx, next.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, first.RefreshToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("a used refresh token: %v", err)
	}
	c.add(2*time.Hour + types.DefaultRefreshGrace)
	if _, err := s.Refresh(ctx, next.RefreshToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("after the grace: %v", err)
	}
}

// TestRotation: tokens signed before a rotation keep verifying until the
// latest expiry of the tokens stored then, and for ever when one of them
// never expires; tokens signed after use the new key.
func TestRotation(t *testing.T) {
	s, db, c := world(t)
	ctx := context.Background()
	before := issue(t, s, Request{ExpiresIn: dur(time.Hour)})
	oldKID := s.keys.active
	if err := s.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	after := issue(t, s, Request{ExpiresIn: dur(3 * time.Hour)})
	if s.keys.active == oldKID {
		t.Fatal("rotation kept the active key")
	}
	for _, raw := range []string{before.AccessToken, after.AccessToken} {
		if _, err := s.Verify(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	// A second service (another instance) sees the retirement on its first
	// unknown key.
	other := newService(t, db, c)
	if _, err := other.Verify(ctx, after.AccessToken); err != nil {
		t.Fatal(err)
	}
	c.add(2 * time.Hour)
	if err := s.reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.keys.public[oldKID]; ok {
		t.Fatal("the retired key outlived the tokens it signed")
	}
	if _, err := s.Verify(ctx, after.AccessToken); err != nil {
		t.Fatal(err)
	}

	// With a token that never expires, the retired key stays.
	s2, _, c2 := world(t)
	forever := issue(t, s2, Request{ExpiresIn: dur(0), ByAdmin: true})
	if err := s2.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	c2.add(365 * 24 * time.Hour)
	if _, err := s2.Verify(ctx, forever.AccessToken); err != nil {
		t.Fatalf("a token that never expires after rotation: %v", err)
	}
}

// TestSharedStore: a second instance on the same database reuses the key
// rather than creating its own, and verifies the first one's tokens.
func TestSharedStore(t *testing.T) {
	s, db, c := world(t)
	out := issue(t, s, Request{})
	other := newService(t, db, c)
	if other.keys.active != s.keys.active {
		t.Fatal("a second instance created another key")
	}
	if _, err := other.Verify(context.Background(), out.AccessToken); err != nil {
		t.Fatal(err)
	}
	keys, err := db.Keys().SigningKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("signing keys %v, %v", keys, err)
	}
}

func TestNewNeedsIdentity(t *testing.T) {
	if _, err := New(
		context.Background(),
		inmem.New(),
		Options{},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("no identity: %v", err)
	}
}

func TestReferenceForm(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		r := newReference()
		if !types.IsReferenceToken(r) || seen[r] {
			t.Fatalf("reference %q", r)
		}
		seen[r] = true
	}
	if u := newUUID(); len(u) != 36 || u[14] != '4' {
		t.Fatalf("uuid %s", u)
	}
}
