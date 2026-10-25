package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestMain makes hashing cheap: the production parameters cost 64 MiB and
// tens of milliseconds per hash, which the suite would pay hundreds of
// times. Parsing the parameters from each hash is tested separately.
func TestMain(m *testing.M) {
	defaultParams = HashParams{MemoryKiB: 64, Time: 1, Threads: 1}
	os.Exit(m.Run())
}

func TestUnitHashAndCheck(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil || !strings.HasPrefix(string(h), "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash %q, %v", h, err)
	}
	if ok, err := CheckPassword(h, "s3cret"); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	if ok, err := CheckPassword(h, "wrong"); ok || err != nil {
		t.Fatalf("wrong password: %v %v", ok, err)
	}
	// A hash made with other parameters still verifies: they are read
	// from the hash, not assumed.
	other, err := HashPasswordWith(HashParams{MemoryKiB: 128, Time: 2, Threads: 2}, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword(other, "s3cret"); !ok || err != nil {
		t.Fatalf("other parameters: %v %v", ok, err)
	}
	h2, _ := HashPassword("s3cret")
	if string(h) == string(h2) {
		t.Fatal("two hashes of one password share a salt")
	}
}

// TestUnitProductionHashParams pins the production cost (RFC 9106's second
// recommended option). TestMain lowers defaultParams for speed, so the pin
// reads productionParams, which nothing can change.
func TestUnitProductionHashParams(t *testing.T) {
	if got := productionParams(); got != (HashParams{MemoryKiB: 64 << 10, Time: 3, Threads: 4}) {
		t.Fatalf("production hash parameters changed: %+v", got)
	}
}

func TestUnitHashParamsReachTheHashAndTheDummy(t *testing.T) {
	cheap := HashParams{MemoryKiB: 32, Time: 1, Threads: 1}
	h, err := HashPasswordWith(cheap, "pw")
	if err != nil || !strings.HasPrefix(string(h), "$argon2id$v=19$m=32,t=1,p=1$") {
		t.Fatalf("hash %q, %v", h, err)
	}
	db := inmem.New()
	a, err := NewAuthenticator(db.Users(), db.Settings(), Options{Hash: cheap})
	if err != nil || !strings.HasPrefix(string(a.dummy), "$argon2id$v=19$m=32,t=1,p=1$") {
		t.Fatalf("dummy %q, %v", a.dummy, err)
	}
	if _, err := SeedHashed(context.Background(), db, h, time.Now()); err != nil {
		t.Fatal(err)
	}
	admin, err := db.Users().Get(context.Background(), AdminUser)
	if err != nil || string(admin.PasswordHash) != string(h) {
		t.Fatalf("admin hash %q, %v", admin.PasswordHash, err)
	}
	if now, err := SeedHashed(context.Background(), db, h, time.Now()); now || err != nil {
		t.Fatalf("second seed: %v %v", now, err)
	}
}

func TestUnitCheckPasswordRejectsMalformedHashes(t *testing.T) {
	for _, h := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=16$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=x,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=2000000,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=0,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=0$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$!!$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$!!",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$",
		"x$argon2id$v=19$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$a2V5", // a 3-byte key
	} {
		if _, err := CheckPassword([]byte(h), "pw"); !errors.Is(err, ErrHashFormat) {
			t.Errorf("%q: %v", h, err)
		}
	}
}

func TestUnitRequire(t *testing.T) {
	ctx := context.Background()
	user := WithPrincipal(ctx, Principal{Name: "dev", Groups: []string{"readers"}})
	admin := WithPrincipal(ctx, Principal{Name: "admin", Admin: true})
	anon := WithPrincipal(ctx, Anonymous)
	if _, ok := FromContext(ctx); ok {
		t.Fatal("a bare context has a principal")
	}
	for name, tc := range map[string]struct {
		ctx       context.Context
		authn     error
		adminOnly error
	}{
		"none":      {ctx, ErrUnauthenticated, ErrUnauthenticated},
		"anonymous": {anon, ErrUnauthenticated, ErrUnauthenticated},
		"user":      {user, nil, ErrForbidden},
		"admin":     {admin, nil, nil},
	} {
		if _, err := RequireAuthenticated(tc.ctx); !errors.Is(err, tc.authn) {
			t.Errorf("%s: RequireAuthenticated = %v", name, err)
		}
		if _, err := RequireAdmin(tc.ctx); !errors.Is(err, tc.adminOnly) {
			t.Errorf("%s: RequireAdmin = %v", name, err)
		}
	}
	if p, _ := RequireAdmin(admin); p.Name != "admin" {
		t.Fatalf("RequireAdmin returned %+v", p)
	}
	var e *apierror.Error
	if !errors.As(ErrForbidden, &e) || e.Status != http.StatusForbidden ||
		ErrBadCredentials.Status != http.StatusUnauthorized {
		t.Fatal("errors carry the wrong status")
	}
}

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newAuth(t *testing.T, db *inmem.Store, o Options) (*Authenticator, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	if o.Now == nil {
		o.Now = c.now
	}
	a, err := NewAuthenticator(db.Users(), db.Settings(), o)
	if err != nil {
		t.Fatal(err)
	}
	return a, c
}

func addUser(t *testing.T, db *inmem.Store, u types.User, password string) {
	t.Helper()
	if password != "" {
		h, err := HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		u.PasswordHash = h
	}
	if err := db.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}

func lastLogin(t *testing.T, db *inmem.Store, name string) time.Time {
	t.Helper()
	u, err := db.Users().Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.LastLoggedIn
}

func TestUnitBasic(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if err := db.Groups().Create(ctx, types.Group{Name: "readers"}); err != nil {
		t.Fatal(err)
	}
	addUser(
		t,
		db,
		types.User{Name: "dev", Status: types.UserEnabled, Groups: []string{"readers"}},
		"pw",
	)
	addUser(t, db, types.User{Name: "boss", Admin: true}, "pw")
	addUser(t, db, types.User{Name: "gone", Status: types.UserDisabled}, "pw")
	addUser(t, db, types.User{Name: "sso", InternalPasswordDisabled: true}, "pw")
	addUser(t, db, types.User{Name: "nopass"}, "")
	addUser(t, db, types.User{Name: "corrupt", PasswordHash: []byte("$argon2id$garbage")}, "")
	a, clk := newAuth(t, db, Options{})

	p, err := a.Basic(ctx, "dev", "pw")
	if err != nil || p.Name != "dev" || p.Admin || p.Anonymous || len(p.Groups) != 1 ||
		!p.Password {
		t.Fatalf("dev = %+v, %v", p, err)
	}
	if p, err := a.Basic(ctx, "boss", "pw"); err != nil || !p.Admin {
		t.Fatalf("boss = %+v, %v", p, err)
	}
	for _, c := range [][2]string{
		{"dev", "wrong"},
		{"nobody", "pw"},
		{"gone", "pw"},
		{"sso", "pw"},
		{"nopass", ""},
		{"corrupt", "pw"},
	} {
		if _, err := a.Basic(ctx, c[0], c[1]); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%s/%s: %v", c[0], c[1], err)
		}
	}

	// The login is recorded when the cache fills, not on every request.
	first := lastLogin(t, db, "dev")
	if !first.Equal(clk.t) {
		t.Fatalf("last login %v, want %v", first, clk.t)
	}
	clk.t = clk.t.Add(time.Minute)
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatal(err)
	}
	if !lastLogin(t, db, "dev").Equal(first) {
		t.Fatal("a cached login was recorded again")
	}
	// After the TTL the password is verified, and recorded, again.
	clk.t = clk.t.Add(DefaultCacheTTL)
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatal(err)
	}
	if !lastLogin(t, db, "dev").Equal(clk.t) {
		t.Fatal("an expired cache entry did not verify again")
	}

	// A password change misses the cache by construction.
	u, _ := db.Users().Get(ctx, "dev")
	u.PasswordHash, _ = HashPassword("new")
	if err := db.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("old password after a change: %v", err)
	}
	if _, err := a.Basic(ctx, "dev", "new"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

// failingUsers fails Get, as a database that went away would.
type failingUsers struct{ store.UserStore }

func (failingUsers) Get(context.Context, string) (types.User, error) {
	return types.User{}, errors.New("database down")
}

// failingSettings fails Get.
type failingSettings struct{ store.SettingsStore }

func (failingSettings) Get(
	context.Context,
	string,
	any,
) error {
	return errors.New("database down")
}

func TestUnitBasicErrors(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	a, err := NewAuthenticator(failingUsers{db.Users()}, db.Settings(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Fatalf("store failure = %v, want a server error", err)
	}

	// With every hashing slot taken, a request gives up when its context
	// ends rather than queueing for ever.
	addUser(t, db, types.User{Name: "dev"}, "pw")
	b, _ := newAuth(t, db, Options{Concurrency: 1})
	b.slots <- struct{}{}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Basic(cancelled, "dev", "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled while waiting: %v", err)
	}
	<-b.slots
}

func TestUnitAnonymousAllowed(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	a, _ := newAuth(t, db, Options{})
	if on, err := a.AnonymousAllowed(ctx); on || err != nil {
		t.Fatalf("unset = %v, %v", on, err)
	}
	if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if on, err := a.AnonymousAllowed(ctx); !on || err != nil {
		t.Fatalf("on = %v, %v", on, err)
	}
	f, err := NewAuthenticator(db.Users(), failingSettings{db.Settings()}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AnonymousAllowed(ctx); err == nil {
		t.Fatal("a settings failure was read as off")
	}
}

func TestUnitCacheBound(t *testing.T) {
	now := time.Now()
	c := &verified{max: 2, ttl: time.Minute, m: map[[32]byte]time.Time{}}
	k := func(b byte) [32]byte { return [32]byte{b} }
	c.add(k(1), now)
	c.add(k(2), now.Add(-2*time.Minute)) // already expired
	c.add(k(3), now)                     // full: the expired entry goes
	if len(c.m) != 2 || !c.has(k(1), now) || !c.has(k(3), now) {
		t.Fatalf("after expiry eviction: %v", c.m)
	}
	c.add(k(4), now) // full of live entries: one is dropped
	if len(c.m) != 2 || !c.has(k(4), now) {
		t.Fatalf("after forced eviction: %v", c.m)
	}
}
