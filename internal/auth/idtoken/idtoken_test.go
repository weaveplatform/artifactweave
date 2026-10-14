package idtoken_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/auth/idtoken/idtokentest"
)

const aud = "artifactweave"

func issuer(t *testing.T) *idtokentest.Issuer {
	t.Helper()
	i, err := idtokentest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(i.Close)
	return i
}

func mint(t *testing.T, i *idtokentest.Issuer, ttl time.Duration, extra map[string]any) string {
	t.Helper()
	raw, err := i.Mint(aud, ttl, extra)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// clock is a settable time source.
type clock struct{ now atomic.Int64 }

func (c *clock) Now() time.Time      { return time.Unix(0, c.now.Load()) }
func (c *clock) add(d time.Duration) { c.now.Add(int64(d)) }

func newClock() *clock {
	c := &clock{}
	c.now.Store(time.Now().UnixNano())
	return c
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	i := issuer(t)
	v := idtoken.New(idtoken.Options{})
	p := idtoken.Provider{URL: i.URL() + "/", Audience: aud}
	claims, err := v.Verify(ctx, mint(t, i, time.Hour, map[string]any{"sub": "repo:o/r"}), p)
	if err != nil || claims["sub"] != "repo:o/r" {
		t.Fatalf("Verify = %v, %v", claims, err)
	}
	// An audience list that holds the audience verifies, and a provider
	// without an audience takes any.
	list := mint(t, i, time.Hour, map[string]any{"aud": []string{"other", aud}})
	if _, err := v.Verify(ctx, list, p); err != nil {
		t.Fatalf("aud list: %v", err)
	}
	other := mint(t, i, time.Hour, map[string]any{"aud": "someone-else"})
	if _, err := v.Verify(ctx, other, idtoken.Provider{URL: i.URL()}); err != nil {
		t.Fatalf("no audience configured: %v", err)
	}
	// Issuer overrides the iss a token must carry, as Access's token_issuer.
	moved := mint(t, i, time.Hour, map[string]any{"iss": "https://ci.example/"})
	if _, err := v.Verify(ctx, moved, idtoken.Provider{
		URL: i.URL(), Issuer: "https://ci.example",
	}); err != nil {
		t.Fatalf("token issuer: %v", err)
	}
	if got := i.Fetches(); got != 1 {
		t.Fatalf("keys fetched %d times, want once while cached", got)
	}
}

func TestVerifyRefuses(t *testing.T) {
	ctx := context.Background()
	i := issuer(t)
	v := idtoken.New(idtoken.Options{})
	p := idtoken.Provider{URL: i.URL(), Audience: aud}
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := idtokentest.SignRS256(stranger, "key-1", map[string]any{
		"iss": i.URL(), "aud": aud, "exp": time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	hs, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	symmetric, err := jwt.Signed(hs).Claims(map[string]any{"iss": i.URL(), "aud": aud}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"not a JWT":      "nonsense",
		"HS256":          symmetric,
		"forged":         forged,
		"other issuer":   mint(t, i, time.Hour, map[string]any{"iss": "https://elsewhere"}),
		"other audience": mint(t, i, time.Hour, map[string]any{"aud": "someone-else"}),
		"expired":        mint(t, i, -2*time.Minute, nil),
		"no expiry":      mint(t, i, time.Hour, map[string]any{"exp": nil}),
		"not yet valid":  mint(t, i, time.Hour, map[string]any{"nbf": time.Now().Add(time.Hour).Unix()}),
	} {
		if _, err := v.Verify(ctx, raw, p); !errors.Is(err, idtoken.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestKeyRotation: a token signed by a key not yet cached makes the verifier
// fetch again, but no more often than every 30 seconds.
func TestKeyRotation(t *testing.T) {
	ctx := context.Background()
	i := issuer(t)
	c := newClock()
	v := idtoken.New(idtoken.Options{Now: c.Now})
	p := idtoken.Provider{URL: i.URL(), Audience: aud}
	if _, err := v.Verify(ctx, mint(t, i, time.Hour, nil), p); err != nil {
		t.Fatal(err)
	}
	if err := i.Rotate(); err != nil {
		t.Fatal(err)
	}
	rotated := mint(t, i, time.Hour, nil)
	// Within 30 seconds of the last fetch the new key is not looked for.
	if _, err := v.Verify(ctx, rotated, p); !errors.Is(err, idtoken.ErrInvalid) {
		t.Fatalf("too soon: %v", err)
	}
	c.add(31 * time.Second)
	if _, err := v.Verify(ctx, rotated, p); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if got := i.Fetches(); got != 2 {
		t.Fatalf("fetches = %d, want 2", got)
	}
	// After the cache lifetime the keys are fetched again.
	c.add(idtoken.DefaultKeyTTL)
	if _, err := v.Verify(ctx, rotated, p); err != nil || i.Fetches() != 3 {
		t.Fatalf("stale cache: %v, fetches %d", err, i.Fetches())
	}
}

func TestKeysUnavailable(t *testing.T) {
	ctx := context.Background()
	i := issuer(t)
	c := newClock()
	v := idtoken.New(idtoken.Options{Now: c.Now, KeyTTL: time.Hour})
	p := idtoken.Provider{URL: i.URL(), Audience: aud}
	raw := mint(t, i, 3*time.Hour, nil)

	i.FailDiscovery.Store(true)
	if _, err := v.Verify(ctx, raw, p); !errors.Is(err, idtoken.ErrKeys) {
		t.Fatalf("discovery down: %v", err)
	}
	i.FailDiscovery.Store(false)
	i.FailKeys.Store(true)
	if _, err := v.Verify(ctx, raw, p); !errors.Is(err, idtoken.ErrKeys) {
		t.Fatalf("JWKS down: %v", err)
	}
	i.FailKeys.Store(false)
	if _, err := v.Verify(ctx, raw, p); err != nil {
		t.Fatal(err)
	}
	// A provider down while its cached keys are still trusted keeps them,
	// even for a forced fetch.
	i.FailKeys.Store(true)
	c.add(time.Minute)
	if err := i.Rotate(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, raw, p); err != nil {
		t.Fatalf("cached keys while the provider is down: %v", err)
	}
	if _, err := v.Verify(ctx, mint(t, i, time.Hour, nil), p); !errors.Is(err, idtoken.ErrInvalid) {
		t.Fatalf("unknown key while the provider is down: %v", err)
	}
	// Past the cache lifetime nothing is trusted.
	c.add(time.Hour)
	if _, err := v.Verify(ctx, raw, p); !errors.Is(err, idtoken.ErrKeys) {
		t.Fatalf("stale keys while the provider is down: %v", err)
	}
}

// TestAzureAppID asks the JWKS for an application's signing keys.
func TestAzureAppID(t *testing.T) {
	i := issuer(t)
	v := idtoken.New(idtoken.Options{})
	p := idtoken.Provider{URL: i.URL(), Audience: aud, AppID: "app-1"}
	if _, err := v.Verify(context.Background(), mint(t, i, time.Hour, nil), p); err != nil {
		t.Fatal(err)
	}
	if got := i.AppIDs(); len(got) != 1 || got[0] != "app-1" {
		t.Fatalf("appid = %v", got)
	}
}

// countingTransport counts the requests a client sends.
type countingTransport struct{ n atomic.Int64 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r) //nolint:wrapcheck // a test transport
}

// TestProxyChoice fetches through Client when the provider uses the default
// proxy and through Direct when it does not.
func TestProxyChoice(t *testing.T) {
	ctx := context.Background()
	i := issuer(t)
	proxied, direct := &countingTransport{}, &countingTransport{}
	v := idtoken.New(idtoken.Options{
		Client: &http.Client{Transport: proxied}, Direct: &http.Client{Transport: direct},
	})
	raw := mint(t, i, time.Hour, nil)
	if _, err := v.Verify(ctx, raw, idtoken.Provider{URL: i.URL(), UseProxy: true}); err != nil {
		t.Fatal(err)
	}
	if proxied.n.Load() != 2 || direct.n.Load() != 0 {
		t.Fatalf("proxied %d, direct %d", proxied.n.Load(), direct.n.Load())
	}
}

// TestBadDocuments covers providers that answer, but not with keys.
func TestBadDocuments(t *testing.T) {
	ctx := context.Background()
	i := issuer(t)
	raw := mint(t, i, time.Hour, nil)
	for name, discovery := range map[string]string{
		"no jwks_uri":     `{}`,
		"not JSON":        `{`,
		"bad jwks_uri":    `{"jwks_uri":"::not a url"}`,
		"too large":       `{"pad":"` + strings.Repeat("x", 1<<20) + `"}`,
		"jwks not served": `{"jwks_uri":"{base}/missing"}`,
	} {
		r := chi.NewRouter()
		var base string
		r.Get("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.ReplaceAll(discovery, "{base}", base)))
		})
		srv := httptest.NewServer(r)
		base = srv.URL
		v := idtoken.New(idtoken.Options{})
		// The app id forces the jwks_uri through url.Parse.
		_, err := v.Verify(ctx, raw, idtoken.Provider{URL: srv.URL, Issuer: i.URL(), AppID: "a"})
		srv.Close()
		if !errors.Is(err, idtoken.ErrKeys) {
			t.Errorf("%s: %v", name, err)
		}
	}
	unreachable := idtoken.New(idtoken.Options{})
	if _, err := unreachable.Verify(
		ctx,
		raw,
		idtoken.Provider{URL: "http://127.0.0.1:1"},
	); !errors.Is(
		err,
		idtoken.ErrKeys,
	) {
		t.Errorf("unreachable: %v", err)
	}
	if _, err := unreachable.Verify(
		ctx,
		raw,
		idtoken.Provider{URL: "http://bad host"},
	); !errors.Is(
		err,
		idtoken.ErrKeys,
	) {
		t.Errorf("bad URL: %v", err)
	}
}

// TestKeyWithoutID verifies a token whose header names no key against every
// published key of its algorithm.
func TestKeyWithoutID(t *testing.T) {
	ctx := context.Background()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	r.Get("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jwks_uri":"` + srv.URL + `/keys"}`))
	})
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: k.Public(), Algorithm: string(jose.ES256)}, // skipped: another algorithm
		{Key: k.Public(), Algorithm: string(jose.RS256)},
	}}
	r.Get("/keys", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := set.Keys[0].MarshalJSON()
		c, _ := set.Keys[1].MarshalJSON()
		_, _ = w.Write([]byte(`{"keys":[` + string(b) + `,` + string(c) + `]}`))
	})
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(s).Claims(map[string]any{
		"iss": srv.URL, "exp": time.Now().Add(time.Hour).Unix(),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idtoken.New(idtoken.Options{}).
		Verify(ctx, raw, idtoken.Provider{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
}
