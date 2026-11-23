package ldap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Defaults of LoginOptions.
const (
	// DefaultLoginTTL is how long an accepted login is answered without the
	// directory: JFrog's non-UI authentication cache for LDAP, whose
	// artifactory.security.authentication.cache.idleTimeSecs defaults to
	// 300 seconds (the LDAP page, "Non-UI Authentication Cache for LDAP").
	// The JFrog CLI sends its password on every request, so without it
	// every request would bind.
	DefaultLoginTTL = 5 * time.Minute
	// DefaultLoginCacheSize bounds the remembered logins, as
	// authn.DefaultCacheSize bounds the internal ones.
	DefaultLoginCacheSize = 10_000
)

// LoginOptions configures NewLogins.
type LoginOptions struct {
	// TTL is how long an accepted login is remembered; zero or less means
	// DefaultLoginTTL.
	TTL time.Duration
	// CacheSize bounds the remembered logins; zero or less means
	// DefaultLoginCacheSize.
	CacheSize int
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Log receives the directory failures, by connection key and step,
	// never a password; nil means slog.Default().
	Log *slog.Logger
}

// Logins checks passwords against the enabled LDAP connections for the
// authenticator (authn.Directory). It reads the connections through
// Settings on every login, so a connection disabled, deleted or changed is
// never used again, and remembers accepted logins only under the
// connection revision that accepted them.
type Logins struct {
	settings  Service
	directory Directory
	ttl       time.Duration
	now       func() time.Time
	log       *slog.Logger
	cache     *loginCache
}

var _ authn.Directory = (*Logins)(nil)

// NewLogins returns Logins over settings, whose Tx and Guard Snapshot needs,
// and the directory client.
func NewLogins(settings Service, directory Directory, o LoginOptions) *Logins {
	if o.TTL <= 0 {
		o.TTL = DefaultLoginTTL
	}
	if o.CacheSize <= 0 {
		o.CacheSize = DefaultLoginCacheSize
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	var salt [32]byte
	_, _ = rand.Read(salt[:])
	return &Logins{
		settings: settings, directory: directory, ttl: o.TTL, now: o.Now, log: o.Log,
		cache: &loginCache{max: o.CacheSize, salt: salt, m: map[[32]byte]cachedLogin{}},
	}
}

// Proof binds a login of name to the connection key and revision that
// accepted it (authn.DirectoryLogin.Proof). The revision is random and
// changes on every write of the connection, so a proof never outlives the
// settings it was made under. A key holds no NUL (validate) and a revision
// is hex, so with the name last the fields cannot run together.
func Proof(key, revision, name string) [32]byte {
	return sha256.Sum256([]byte("ldap\x00" + key + "\x00" + revision + "\x00" + name))
}

// Login implements authn.Directory: it asks the enabled connections, in key
// order, until one accepts. The order is a local choice [I]: JFrog keeps its
// connections in descriptor order, which the Access API does not expose.
func (l *Logins) Login(ctx context.Context, name, password string) (authn.DirectoryLogin, error) {
	all, err := l.settings.all(ctx)
	if err != nil {
		return authn.DirectoryLogin{}, err
	}
	enabled := make(map[string]stored, len(all))
	for _, st := range all {
		if st.conn.Enabled {
			enabled[st.conn.Key] = st
		}
	}
	if len(enabled) == 0 {
		return authn.DirectoryLogin{}, authn.ErrNoDirectory
	}
	if password == "" {
		// An empty password is an unauthenticated bind (RFC 4513 section
		// 5.1.2), which many directories accept for any DN.
		return authn.DirectoryLogin{}, kernel.ErrBadCredentials
	}
	now := l.now()
	key := l.cache.key(name, password)
	if hit, ok := l.cache.get(key, now); ok {
		if st, ok := enabled[hit.conn]; ok && Proof(st.conn.Key, st.revision, name) == hit.proof {
			return login(st, name, hit.email, false), nil
		}
		l.cache.drop(key)
	}
	// Snapshot pairs each connection with its own password; the list above
	// told only which connections are enabled and their revisions.
	snap, err := l.settings.Snapshot(ctx)
	if err != nil {
		return authn.DirectoryLogin{}, err
	}
	var failed error
	refused := false
	for _, c := range snap.Connections {
		if !c.Enabled {
			continue
		}
		entry, err := l.directory.Authenticate(ctx, c, name, password)
		switch {
		case err == nil:
			st := stored{conn: c.LDAPConnection, revision: c.Revision}
			l.cache.put(key, cachedLogin{
				conn: c.Key, proof: Proof(c.Key, c.Revision, name), email: entry.Email, expires: now.Add(l.ttl),
			})
			return login(st, name, entry.Email, true), nil
		case errors.Is(err, ErrUnknownUser), errors.Is(err, ErrRefused):
			refused = true
		default:
			l.log.WarnContext(ctx, "LDAP login could not reach the directory",
				"connection", c.Key, "error", err.Error())
			failed = errors.Join(failed, fmt.Errorf("ldap: connection %s: %w", c.Key, err))
		}
	}
	if refused || failed == nil {
		// One connection's definite refusal answers for the name, even
		// while another is down: the password was wrong where it was asked.
		return authn.DirectoryLogin{}, kernel.ErrBadCredentials
	}
	return authn.DirectoryLogin{}, failed
}

func login(st stored, name, email string, fresh bool) authn.DirectoryLogin {
	return authn.DirectoryLogin{
		Proof: Proof(st.conn.Key, st.revision, name), Email: email, AutoCreate: st.conn.AutoCreateUser,
		ProfileUpdatable: st.conn.AllowUserToAccessProfile, Fresh: fresh,
	}
}

// Current implements authn.Directory. It reads the connections in the
// transaction ctx carries; the authenticator's publication holds the
// administration guard there, which every connection write also holds.
func (l *Logins) Current(ctx context.Context, name string, proof [32]byte) (authn.DirectoryLogin, error) {
	all, err := l.settings.all(ctx)
	if err != nil {
		return authn.DirectoryLogin{}, err
	}
	for _, st := range all {
		if st.conn.Enabled && Proof(st.conn.Key, st.revision, name) == proof {
			return login(st, name, "", false), nil
		}
	}
	return authn.DirectoryLogin{}, kernel.ErrBadCredentials
}

// Enabled implements authn.Directory: whether any connection is enabled.
func (l *Logins) Enabled(ctx context.Context) (bool, error) {
	all, err := l.settings.all(ctx)
	if err != nil {
		return false, err
	}
	for _, st := range all {
		if st.conn.Enabled {
			return true, nil
		}
	}
	return false, nil
}

// cachedLogin is a remembered login: the connection that accepted it, its
// proof under the revision of the time, and the email read then.
type cachedLogin struct {
	conn    string
	proof   [32]byte
	email   string
	expires time.Time
}

// loginCache is a bounded map of remembered logins. Its keys are salted
// digests of the name and password, so the memory of the process holds no
// password and no digest another process could match.
type loginCache struct {
	mu   sync.Mutex
	max  int
	salt [32]byte
	m    map[[32]byte]cachedLogin
}

func (c *loginCache) key(name, password string) [32]byte {
	h := sha256.New()
	h.Write(c.salt[:])
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(password))
	var k [32]byte
	h.Sum(k[:0])
	return k
}

func (c *loginCache) get(k [32]byte, now time.Time) (cachedLogin, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if ok && !now.Before(v.expires) {
		delete(c.m, k)
		return cachedLogin{}, false
	}
	return v, ok
}

func (c *loginCache) drop(k [32]byte) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

func (c *loginCache) put(k [32]byte, v cachedLogin) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Full: drop arbitrary entries (map order is random); each costs its
	// owner one more bind.
	for key := range c.m {
		if len(c.m) < c.max {
			break
		}
		delete(c.m, key)
	}
	c.m[k] = v
}
