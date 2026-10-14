package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Defaults for the zero Options fields.
const (
	// DefaultCacheSize bounds the verified-credentials cache. An entry is a
	// 32-byte key and an expiry, so the bound costs well under a megabyte.
	DefaultCacheSize = 10_000
	// DefaultCacheTTL is how long a verified password skips Argon2. The JFrog
	// CLI sends Basic credentials on every request, so without the cache a
	// large upload would pay 64 MiB and tens of milliseconds per call.
	DefaultCacheTTL = 5 * time.Minute
)

// Options configures NewAuthenticator.
type Options struct {
	CacheSize int
	CacheTTL  time.Duration
	// Concurrency bounds the password hashes computed at once; zero means
	// GOMAXPROCS. Each costs the hash's memory parameter, so without a bound
	// a flood of wrong passwords could exhaust memory.
	Concurrency int
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Hash is the cost of the dummy hash checked for unknown users; the
	// zero value means the production parameters. Tests set tiny costs.
	Hash HashParams
	// Tokens verifies access and reference tokens; nil refuses every token.
	Tokens TokenVerifier
}

// Authenticator verifies credentials against the user store.
type Authenticator struct {
	users    store.UserStore
	settings store.SettingsStore
	now      func() time.Time
	cache    *verified
	slots    chan struct{}
	// dummy is checked for unknown users, so the time a wrong answer takes
	// does not tell which user names exist.
	dummy  []byte
	tokens TokenVerifier
	policy policyCache
}

// NewAuthenticator returns an Authenticator over users and settings.
func NewAuthenticator(
	users store.UserStore,
	settings store.SettingsStore,
	o Options,
) (*Authenticator, error) {
	var pw [16]byte
	_, _ = rand.Read(pw[:])
	dummy, err := HashPasswordWith(o.Hash, string(pw[:]))
	if err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheSize <= 0 {
		o.CacheSize = DefaultCacheSize
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.Concurrency <= 0 {
		o.Concurrency = runtime.GOMAXPROCS(0)
	}
	return &Authenticator{
		users: users, settings: settings, now: o.Now, dummy: dummy, tokens: o.Tokens,
		cache: &verified{max: o.CacheSize, ttl: o.CacheTTL, m: map[[32]byte]time.Time{}},
		slots: make(chan struct{}, o.Concurrency),
	}, nil
}

// Basic verifies a user name and password. Wrong credentials, an unknown or
// disabled user, and a user without an internal password all answer
// ErrBadCredentials alike; while basic authentication is switched off,
// every password answers ErrBasicAuthDisabled. A correct but expired
// password answers ErrPasswordExpired with the principal, marked
// PasswordExpired.
func (a *Authenticator) Basic(ctx context.Context, name, password string) (Principal, error) {
	// A switch that cannot be read does not stop the check here: a correct
	// password then fails in accept, which needs the same settings, and a
	// wrong one still answers as a wrong password.
	if enabled, err := a.basicEnabled(ctx); err == nil && !enabled {
		return Principal{}, ErrBasicAuthDisabled
	}
	u, err := a.users.Get(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		_, _ = a.check(ctx, a.dummy, password)
		return Principal{}, ErrBadCredentials
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: user %s: %w", name, err)
	}
	if u.Status == types.UserLocked {
		// Answered like a wrong password, after the same work, so neither
		// the message nor the timing tells that the account exists.
		_, _ = a.check(ctx, a.dummy, password)
		return Principal{}, ErrBadCredentials
	}
	if len(u.PasswordHash) == 0 || u.InternalPasswordDisabled ||
		(u.Status != "" && u.Status != types.UserEnabled) {
		return Principal{}, ErrBadCredentials
	}
	now := a.now()
	key := cacheKey(u.PasswordHash, password)
	if a.cache.has(key, now) {
		return a.accept(ctx, u, now, false)
	}
	ok, err := a.check(ctx, u.PasswordHash, password)
	switch {
	case errors.Is(err, ErrHashFormat), err == nil && !ok:
		a.recordFailure(ctx, u.Name)
		return Principal{}, ErrBadCredentials
	case err != nil:
		return Principal{}, err
	}
	a.cache.add(key, now)
	return a.accept(ctx, u, now, true)
}

// AnonymousAllowed reports whether requests without credentials act as the
// anonymous user. The setting is a JSON boolean; unset means off, as in
// Artifactory.
func (a *Authenticator) AnonymousAllowed(ctx context.Context) (bool, error) {
	var on bool
	err := a.settings.Get(ctx, types.SettingAnonymousAccess, &on)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("auth: anonymous access setting: %w", err)
	}
	return on, nil
}

func (a *Authenticator) check(ctx context.Context, stored []byte, password string) (bool, error) {
	select {
	case a.slots <- struct{}{}:
	case <-ctx.Done():
		return false, fmt.Errorf("auth: waiting to verify a password: %w", ctx.Err())
	}
	defer func() { <-a.slots }()
	return CheckPassword(stored, password)
}

func principalOf(u types.User) Principal {
	return Principal{Name: u.Name, Admin: u.Admin, Groups: slices.Clone(u.Groups)}
}

// cacheKey binds a verified password to the stored hash it matched, so a
// password change (a new hash and salt) misses the cache by construction.
func cacheKey(stored []byte, password string) [32]byte {
	h := sha256.New()
	h.Write(stored)
	h.Write([]byte{0})
	h.Write([]byte(password))
	var k [32]byte
	h.Sum(k[:0])
	return k
}

// verified is a bounded set of recently verified credentials with expiry.
type verified struct {
	mu  sync.Mutex
	max int
	ttl time.Duration
	m   map[[32]byte]time.Time
}

func (c *verified) has(k [32]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.m[k]
	if ok && !now.Before(exp) {
		delete(c.m, k)
		ok = false
	}
	return ok
}

func (c *verified) add(k [32]byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		for key, exp := range c.m {
			if !now.Before(exp) {
				delete(c.m, key)
			}
		}
		// Still full: drop arbitrary entries (map order is random). They
		// only cost their owners one more hash.
		for key := range c.m {
			if len(c.m) < c.max {
				break
			}
			delete(c.m, key)
		}
	}
	c.m[k] = now.Add(c.ttl)
}
