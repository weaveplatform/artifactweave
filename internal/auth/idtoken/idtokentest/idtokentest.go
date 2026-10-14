// Package idtokentest is a fake OpenID Connect provider for tests: it serves
// a discovery document and a JWKS, as GitHub Actions, GitLab and other CI
// systems do, and mints ID tokens signed with its current key.
package idtokentest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Issuer is a running fake provider. Its zero value is not usable; call New.
type Issuer struct {
	srv *httptest.Server

	mu   sync.Mutex
	keys []jose.JSONWebKey // public keys served; the last one signs
	sign *rsa.PrivateKey
	kid  string
	n    int

	// FailDiscovery and FailKeys make the discovery document and the JWKS
	// answer 500, for the paths where keys cannot be fetched.
	FailDiscovery, FailKeys atomic.Bool
	// fetches counts JWKS requests, so tests can see the cache work.
	fetches atomic.Int64
	// appIDs records the appid query of each JWKS request.
	appIDs []string
}

// New starts an issuer with one RSA key. Close it when done.
func New() (*Issuer, error) {
	i := &Issuer{}
	if err := i.Rotate(); err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	r.Get("/.well-known/openid-configuration", i.discovery)
	r.Get("/keys", i.jwks)
	i.srv = httptest.NewServer(r)
	return i, nil
}

// URL is the issuer URL, which its tokens carry as iss.
func (i *Issuer) URL() string { return i.srv.URL }

// Close stops the issuer.
func (i *Issuer) Close() { i.srv.Close() }

// Fetches is how many times the JWKS was fetched.
func (i *Issuer) Fetches() int64 { return i.fetches.Load() }

// AppIDs are the appid queries the JWKS was fetched with, in order.
func (i *Issuer) AppIDs() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.appIDs...)
}

// Rotate adds a new signing key; the old ones stay published, as providers
// keep them while tokens they signed are alive.
func (i *Issuer) Rotate() error {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("idtokentest: key: %w", err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	i.kid = fmt.Sprintf("key-%d", i.n)
	i.sign = k
	i.keys = append(i.keys, jose.JSONWebKey{
		Key: k.Public(), KeyID: i.kid, Algorithm: string(jose.RS256), Use: "sig",
	})
	return nil
}

// Mint signs an ID token from this issuer for audience, valid for ttl, with
// extra claims on top of iss, aud, iat, nbf and exp. A negative ttl mints a
// token that has already expired.
func (i *Issuer) Mint(audience string, ttl time.Duration, extra map[string]any) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss": i.URL(),
		"aud": audience,
		"iat": now.Unix(),
		"nbf": now.Add(-time.Minute).Unix(),
		"exp": now.Add(ttl).Unix(),
	}
	for k, v := range extra {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	i.mu.Lock()
	key, kid := i.sign, i.kid
	i.mu.Unlock()
	return SignRS256(key, kid, claims)
}

// SignRS256 signs claims with key, naming kid in the header, so tests can
// sign with a key no issuer publishes.
func SignRS256(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	s, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid),
	)
	if err != nil {
		return "", fmt.Errorf("idtokentest: signer: %w", err)
	}
	raw, err := jwt.Signed(s).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("idtokentest: sign: %w", err)
	}
	return raw, nil
}

func (i *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	if i.FailDiscovery.Load() {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"issuer":                                i.URL(),
		"jwks_uri":                              i.URL() + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (i *Issuer) jwks(w http.ResponseWriter, r *http.Request) {
	i.fetches.Add(1)
	i.mu.Lock()
	i.appIDs = append(i.appIDs, r.URL.Query().Get("appid"))
	set := jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), i.keys...)}
	i.mu.Unlock()
	if i.FailKeys.Load() {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	writeJSON(w, set)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
