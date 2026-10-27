package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func (r *Registry) token(ctx context.Context, challenge registryChallenge) (registryToken, error) {
	return r.flights.do(ctx, challenge.key(), func(shared context.Context) (registryToken, error) {
		r.mu.Lock()
		cached := r.tokens[challenge.key()]
		r.mu.Unlock()
		if cached.expires.After(r.now()) {
			return cached, nil
		}
		// The exchange is bounded independently from artifact streams. Detached
		// coalesced work loses caller deadlines, so the total includes queueing too.
		shared, cancel := context.WithTimeout(shared, time.Minute)
		defer cancel()
		release, err := r.capacity.acquire(shared, "registry")
		if err != nil {
			return registryToken{}, err
		}
		defer release()
		token, err := r.exchange(shared, challenge)
		if err != nil {
			return registryToken{}, err
		}
		r.mu.Lock()
		if len(r.tokens) >= 256 {
			clear(r.tokens)
		}
		r.tokens[challenge.key()] = token
		r.mu.Unlock()
		return token, nil
	})
}

func (r *Registry) exchange(
	ctx context.Context,
	challenge registryChallenge,
) (registryToken, error) {
	if err := r.checkRealm(ctx, challenge.realm); err != nil {
		return registryToken{}, err
	}
	target, err := registryTokenURL(challenge)
	if err != nil {
		return registryToken{}, err
	}
	u, err := checkedURL(target)
	if err != nil {
		return registryToken{}, ErrRegistryChallenge
	}
	ctx = context.WithValue(
		ctx,
		registryAuthorizationKey{},
		registryAuthorization{realm: origin(u)},
	)
	res, err := r.client.Do(ctx, Request{
		Method: http.MethodGet, ExternalURL: target,
		Metadata: true, Header: http.Header{"Accept": {"application/json"}},
	})
	if err != nil {
		return registryToken{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return registryToken{}, registryTokenStatus(res, r.now())
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return registryToken{}, safeError(err)
	}
	if len(data) > 1<<20 {
		return registryToken{}, ErrRegistryChallenge
	}
	return decodeRegistryToken(data, challenge.scope, r.now())
}

func registryTokenStatus(res *http.Response, now time.Time) error {
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return ErrRegistryDenied
	}
	failure := &source.HTTPError{Status: res.StatusCode, Kind: source.FailureKindHTTP}
	if res.StatusCode == http.StatusTooManyRequests {
		failure.RetryAfter = retryAfter(res.Header.Get("Retry-After"), now)
	}
	return failure
}

type registryTokenResponse struct {
	Token       string  `json:"token"`
	AccessToken string  `json:"access_token"`
	ExpiresIn   *int64  `json:"expires_in"`
	IssuedAt    string  `json:"issued_at"`
	Scope       *string `json:"scope"`
}

func decodeRegistryToken(data []byte, scope string, now time.Time) (registryToken, error) {
	var wire registryTokenResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&wire) != nil {
		return registryToken{}, ErrRegistryChallenge
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return registryToken{}, ErrRegistryChallenge
	}
	if wire.Token != "" && wire.AccessToken != "" && wire.Token != wire.AccessToken {
		return registryToken{}, ErrRegistryChallenge
	}
	if wire.Token == "" {
		wire.Token = wire.AccessToken
	}
	if !validRegistryToken(wire.Token) {
		return registryToken{}, ErrRegistryChallenge
	}
	if wire.Scope != nil && *wire.Scope != scope {
		return registryToken{}, ErrRegistryDenied
	}
	seconds := int64(60)
	if wire.ExpiresIn != nil {
		seconds = *wire.ExpiresIn
	}
	if seconds <= 0 {
		return registryToken{}, ErrRegistryDenied
	}
	// Never retain a server-controlled token indefinitely or overflow Duration.
	if seconds > 86400 {
		seconds = 86400
	}
	issued := now
	if wire.IssuedAt != "" {
		var err error
		issued, err = time.Parse(time.RFC3339, wire.IssuedAt)
		if err != nil || issued.After(now.Add(5*time.Minute)) {
			return registryToken{}, ErrRegistryChallenge
		}
	}
	expires := issued.Add(time.Duration(seconds) * time.Second)
	skew := min(time.Duration(seconds)*time.Second/10, 5*time.Second)
	expires = expires.Add(-skew)
	if !expires.After(now) {
		return registryToken{}, ErrRegistryDenied
	}
	return registryToken{value: wire.Token, expires: expires}, nil
}

func validRegistryToken(token string) bool {
	if len(token) == 0 || len(token) > 65536 {
		return false
	}
	return strings.IndexFunc(token, func(c rune) bool {
		return (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			!strings.ContainsRune("-._~+/=", c)
	}) < 0
}
