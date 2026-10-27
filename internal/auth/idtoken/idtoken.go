// Package idtoken verifies ID tokens that an external OpenID Connect
// provider, typically a CI system, signed: the subject tokens of the Access
// OIDC token exchange (POST /access/api/v1/oidc/token, research 21). Signing
// keys come from the provider's discovery document and JWKS, cached per
// provider.
package idtoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

var (
	// ErrInvalid reports a token that does not verify: malformed, signed by
	// no key the provider publishes, from another issuer, for another
	// audience, or outside its validity.
	ErrInvalid = errors.New("idtoken: the subject token does not verify")
	// ErrKeys reports a provider whose signing keys cannot be fetched, so no
	// token of it can be verified for now.
	ErrKeys = errors.New("idtoken: the provider's signing keys are unavailable")
)

// Defaults for the zero Options fields.
const (
	// DefaultKeyTTL is how long fetched keys are trusted before they are
	// fetched again. Providers rotate keys with overlap, and a key a token
	// names that is not cached triggers an early fetch anyway.
	DefaultKeyTTL = 15 * time.Minute
	// DefaultTimeout bounds one discovery or JWKS fetch.
	DefaultTimeout = 10 * time.Second
	// leeway absorbs clock skew between the provider and this server.
	leeway = time.Minute
	// refetchAfter is the shortest interval between fetches a token naming
	// an unknown key may force, so a stream of forged key ids cannot make
	// this server hammer the provider.
	refetchAfter = 30 * time.Second
	// maxDocument bounds a discovery document or a JWKS.
	maxDocument = 1 << 20
)

// algorithms are the JWS algorithms accepted. The symmetric HS* and "none"
// are absent: a provider's ID token is verified with its public key only.
var algorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.EdDSA,
}

// Options configures a Verifier.
type Options struct {
	// Client fetches discovery documents and keys when the provider asks for
	// the default proxy, and Direct when it does not. nil means a client
	// with DefaultTimeout that takes its proxy from the environment, and one
	// that uses none.
	Client, Direct *http.Client
	KeyTTL         time.Duration
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
}

// Verifier verifies ID tokens. It is safe for concurrent use.
type Verifier struct {
	client, direct *http.Client
	ttl            time.Duration
	now            func() time.Time

	mu   sync.Mutex
	sets map[string]*keyset
}

// keyset is one provider's cached keys.
type keyset struct {
	keys    jose.JSONWebKeySet
	fetched time.Time
}

// New returns a Verifier.
func New(o Options) *Verifier {
	if o.Client == nil {
		o.Client = &http.Client{
			Timeout:   DefaultTimeout,
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
		}
	}
	if o.Direct == nil {
		o.Direct = &http.Client{Timeout: DefaultTimeout, Transport: &http.Transport{}}
	}
	if o.KeyTTL <= 0 {
		o.KeyTTL = DefaultKeyTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Verifier{
		client: o.Client, direct: o.Direct, ttl: o.KeyTTL, now: o.Now,
		sets: map[string]*keyset{},
	}
}

// Provider is what a token must agree with.
type Provider struct {
	// URL is the provider's issuer URL; its discovery document is at
	// URL/.well-known/openid-configuration.
	URL string
	// Issuer is the iss a token must carry; empty means URL.
	Issuer string
	// Audience, when set, must be among the token's aud.
	Audience string
	// AppID, for Azure, asks the JWKS for an application's custom signing
	// keys (the appid query parameter).
	AppID string
	// UseProxy fetches through the proxy from the environment.
	UseProxy bool
}

// Verify checks raw against p and returns its claims.
func (v *Verifier) Verify(ctx context.Context, raw string, p Provider) (map[string]any, error) {
	tok, err := jwt.ParseSigned(raw, algorithms)
	if err != nil || len(tok.Headers) != 1 {
		return nil, fmt.Errorf("%w: not a signed jwt", ErrInvalid)
	}
	head := tok.Headers[0]
	keys, err := v.keys(ctx, p, false)
	if err != nil {
		return nil, err
	}
	std, claims, err := verifyWith(tok, keys, head)
	if errors.Is(err, errNoKey) {
		// The provider may have rotated: fetch once more, unless that was
		// just done.
		if keys, err = v.keys(ctx, p, true); err != nil {
			return nil, err
		}
		std, claims, err = verifyWith(tok, keys, head)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := check(std, p, v.now()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return claims, nil
}

var (
	errNoKey    = errors.New("idtoken: signed by no key the provider publishes")
	errIssuer   = errors.New("idtoken: issued by another issuer")
	errAudience = errors.New("idtoken: issued for another audience")
	errNoExpiry = errors.New("idtoken: the token has no expiry")
)

// verifyWith checks tok's signature against the key head names, or against
// every key of its algorithm when it names none.
func verifyWith(
	tok *jwt.JSONWebToken,
	keys jose.JSONWebKeySet,
	head jose.Header,
) (jwt.Claims, map[string]any, error) {
	candidates := keys.Keys
	if head.KeyID != "" {
		candidates = keys.Key(head.KeyID)
	}
	for _, k := range candidates {
		if k.Algorithm != "" && k.Algorithm != head.Algorithm {
			continue
		}
		var (
			std    jwt.Claims
			claims map[string]any
		)
		if err := tok.Claims(k.Key, &std, &claims); err == nil {
			return std, claims, nil
		}
	}
	return jwt.Claims{}, nil, errNoKey
}

// check applies the provider's issuer and audience and the token's times.
// An ID token without an expiry is refused: it would be a credential that
// never ends.
func check(std jwt.Claims, p Provider, now time.Time) error {
	want := p.Issuer
	if want == "" {
		want = p.URL
	}
	if trimSlash(std.Issuer) != trimSlash(want) {
		return fmt.Errorf("%w: %q, not %q", errIssuer, std.Issuer, want)
	}
	if p.Audience != "" && !slices.Contains(std.Audience, p.Audience) {
		return fmt.Errorf("%w: not %q", errAudience, p.Audience)
	}
	if std.Expiry == nil {
		return errNoExpiry
	}
	if err := std.ValidateWithLeeway(jwt.Expected{Time: now}, leeway); err != nil {
		return err //nolint:wrapcheck // the caller wraps it in ErrInvalid
	}
	return nil
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }

// keys returns p's keys: the cached ones while they are fresh, unless force
// asks for a fetch and the last one is not too recent.
func (v *Verifier) keys(ctx context.Context, p Provider, force bool) (jose.JSONWebKeySet, error) {
	id := trimSlash(p.URL) + "\x00" + p.AppID
	now := v.now()
	v.mu.Lock()
	set := v.sets[id]
	v.mu.Unlock()
	var age time.Duration
	if set != nil {
		age = now.Sub(set.fetched)
		if age < refetchAfter || (age < v.ttl && !force) {
			return set.keys, nil
		}
	}
	keys, err := v.fetch(ctx, p)
	if err != nil {
		if set != nil && age < v.ttl {
			// A failed early fetch keeps the keys that are still trusted.
			return set.keys, nil
		}
		return jose.JSONWebKeySet{}, err
	}
	v.mu.Lock()
	v.sets[id] = &keyset{keys: keys, fetched: now}
	v.mu.Unlock()
	return keys, nil
}

// fetch reads p's discovery document and then its JWKS.
func (v *Verifier) fetch(ctx context.Context, p Provider) (jose.JSONWebKeySet, error) {
	client := v.direct
	if p.UseProxy {
		client = v.client
	}
	var disc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := getJSON(
		ctx,
		client,
		trimSlash(p.URL)+"/.well-known/openid-configuration",
		&disc,
	); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	if disc.JWKSURI == "" {
		return jose.JSONWebKeySet{}, fmt.Errorf(
			"%w: the discovery document names no jwks_uri",
			ErrKeys,
		)
	}
	jwks := disc.JWKSURI
	if p.AppID != "" {
		u, err := url.Parse(jwks)
		if err != nil {
			return jose.JSONWebKeySet{}, fmt.Errorf("%w: jwks_uri: %w", ErrKeys, err)
		}
		q := u.Query()
		q.Set("appid", p.AppID)
		u.RawQuery = q.Encode()
		jwks = u.String()
	}
	var set jose.JSONWebKeySet
	if err := getJSON(ctx, client, jwks, &set); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	return set, nil
}

func getJSON(ctx context.Context, client *http.Client, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeys, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeys, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GET %s: status %d", ErrKeys, u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return fmt.Errorf("%w: GET %s: %w", ErrKeys, u, err)
	}
	if len(body) > maxDocument {
		return fmt.Errorf("%w: GET %s: larger than %d bytes", ErrKeys, u, maxDocument)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: GET %s: %w", ErrKeys, u, err)
	}
	return nil
}
