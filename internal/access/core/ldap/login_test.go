package ldap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// scripted is a Directory whose answers each test sets per connection key.
type scripted struct {
	mu      sync.Mutex
	answers map[string]func(c Connection, name, password string) (Entry, error)
	calls   []string
	tested  []Connection
}

func (d *scripted) Authenticate(_ context.Context, c Connection, name, password string) (Entry, error) {
	d.mu.Lock()
	d.calls = append(d.calls, c.Key)
	answer := d.answers[c.Key]
	d.mu.Unlock()
	return answer(c, name, password)
}

func (d *scripted) Test(_ context.Context, c Connection) error {
	d.tested = append(d.tested, c)
	return nil
}

func (d *scripted) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func accepts(user, pw string) func(Connection, string, string) (Entry, error) {
	return func(_ Connection, name, password string) (Entry, error) {
		switch {
		case name != user:
			return Entry{}, ErrUnknownUser
		case password != pw:
			return Entry{}, ErrRefused
		}
		return Entry{DN: "uid=" + name, Email: name + "@example.org"}, nil
	}
}

func unreachable(Connection, string, string) (Entry, error) {
	return Entry{}, &DirectoryError{Op: "connect", Reason: "could not connect"}
}

type loginWorld struct {
	svc    Service
	dir    *scripted
	logins *Logins
	now    *time.Time
	log    *bytes.Buffer
}

func newLoginWorld(t *testing.T) loginWorld {
	t.Helper()
	svc, _ := newService(t)
	now := time.Unix(100, 0)
	dir := &scripted{answers: map[string]func(Connection, string, string) (Entry, error){}}
	var log bytes.Buffer
	logins := NewLogins(svc, dir, LoginOptions{
		Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(&log, nil)),
	})
	return loginWorld{svc: svc, dir: dir, logins: logins, now: &now, log: &log}
}

func (w loginWorld) create(t *testing.T, key string, set func(*accessapi.LDAPChange)) {
	t.Helper()
	ch := searchChange(key)
	if set != nil {
		set(&ch)
	}
	if _, err := w.svc.Create(t.Context(), ch); err != nil {
		t.Fatal(err)
	}
}

func TestUnitLoginsNoDirectory(t *testing.T) {
	w := newLoginWorld(t)
	ctx := t.Context()
	if _, err := w.logins.Login(ctx, "alice", "pw"); !errors.Is(err, authn.ErrNoDirectory) {
		t.Fatalf("no connections: %v", err)
	}
	w.create(t, "corp", func(ch *accessapi.LDAPChange) { ch.Enabled = ptr(false) })
	if _, err := w.logins.Login(ctx, "alice", "pw"); !errors.Is(err, authn.ErrNoDirectory) {
		t.Fatalf("a disabled connection: %v", err)
	}
	if len(w.dir.called()) != 0 {
		t.Fatalf("asked the directory: %v", w.dir.called())
	}
}

func TestUnitLoginsAcceptRefuseAndOrder(t *testing.T) {
	w := newLoginWorld(t)
	ctx := t.Context()
	w.create(t, "b-second", func(ch *accessapi.LDAPChange) {
		ch.AutoCreateUser, ch.AllowUserToAccessProfile = ptr(false), ptr(true)
	})
	w.create(t, "a-first", nil)
	w.dir.answers["a-first"] = accepts("alice", "alice-pw")
	w.dir.answers["b-second"] = accepts("bob", "bob-pw")

	got, err := w.logins.Login(ctx, "bob", "bob-pw")
	if err != nil {
		t.Fatal(err)
	}
	snap := must[Snapshot](t)(w.svc.Snapshot(ctx))
	second, _ := snap.Connection("b-second")
	want := authn.DirectoryLogin{
		Proof: Proof("b-second", second.Revision, "bob"), Email: "bob@example.org",
		AutoCreate: false, ProfileUpdatable: true, Fresh: true,
	}
	if got != want {
		t.Fatalf("login %+v, want %+v", got, want)
	}
	if calls := w.dir.called(); strings.Join(calls, ",") != "a-first,b-second" {
		t.Fatalf("asked %v, want key order", calls)
	}
	// Remembered: the directory is not asked again, and nothing is fresh.
	got, err = w.logins.Login(ctx, "bob", "bob-pw")
	if err != nil || got.Fresh || got.Proof != want.Proof || got.Email != "bob@example.org" {
		t.Fatalf("remembered login %+v, %v", got, err)
	}
	if len(w.dir.called()) != 2 {
		t.Fatalf("asked again: %v", w.dir.called())
	}
	for _, tc := range []struct{ name, password string }{
		{"bob", "wrong"}, {"nobody", "pw"}, {"bob", ""},
	} {
		if _, err := w.logins.Login(ctx, tc.name, tc.password); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatalf("Login(%q, %q) = %v", tc.name, tc.password, err)
		}
	}
	current, err := w.logins.Current(ctx, "bob", want.Proof)
	if err != nil || current.AutoCreate || !current.ProfileUpdatable || current.Proof != want.Proof {
		t.Fatalf("Current = %+v, %v", current, err)
	}
	for _, proof := range [][32]byte{Proof("b-second", second.Revision, "alice"), {}} {
		if _, err := w.logins.Current(ctx, "bob", proof); !errors.Is(err, kernel.ErrBadCredentials) {
			t.Fatalf("Current with another proof: %v", err)
		}
	}
}

func TestUnitLoginsForgetChangedConnections(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, w loginWorld)
	}{
		{"updated", func(t *testing.T, w loginWorld) {
			t.Helper()
			if _, err := w.svc.Replace(ctx, searchChange("corp")); err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled", func(t *testing.T, w loginWorld) {
			t.Helper()
			ch := searchChange("corp")
			ch.Enabled = ptr(false)
			if _, err := w.svc.Replace(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted and created again", func(t *testing.T, w loginWorld) {
			t.Helper()
			if err := w.svc.Delete(ctx, "corp"); err != nil {
				t.Fatal(err)
			}
			w.create(t, "corp", nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newLoginWorld(t)
			w.create(t, "corp", nil)
			w.dir.answers["corp"] = accepts("alice", "pw")
			first, err := w.logins.Login(ctx, "alice", "pw")
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t, w)
			if _, err := w.logins.Current(ctx, "alice", first.Proof); !errors.Is(err, kernel.ErrBadCredentials) {
				t.Fatalf("the old proof is still current: %v", err)
			}
			again, err := w.logins.Login(ctx, "alice", "pw")
			switch {
			case errors.Is(err, authn.ErrNoDirectory):
				return // disabled: nothing to ask
			case err != nil:
				t.Fatal(err)
			case !again.Fresh || again.Proof == first.Proof:
				t.Fatalf("a changed connection answered from memory: %+v", again)
			}
		})
	}
}

func TestUnitLoginsExpireAndBound(t *testing.T) {
	w := newLoginWorld(t)
	ctx := t.Context()
	w.create(t, "corp", nil)
	w.dir.answers["corp"] = accepts("alice", "pw")
	if _, err := w.logins.Login(ctx, "alice", "pw"); err != nil {
		t.Fatal(err)
	}
	*w.now = w.now.Add(DefaultLoginTTL)
	if got, err := w.logins.Login(ctx, "alice", "pw"); err != nil || !got.Fresh {
		t.Fatalf("an expired login answered from memory: %+v, %v", got, err)
	}
	small := NewLogins(w.svc, w.dir, LoginOptions{CacheSize: 1})
	for _, name := range []string{"alice", "bob"} {
		w.dir.answers["corp"] = accepts(name, "pw")
		if _, err := small.Login(ctx, name, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(small.cache.m); n != 1 {
		t.Fatalf("the cache holds %d logins, bound 1", n)
	}
}

func TestUnitLoginsUnavailable(t *testing.T) {
	w := newLoginWorld(t)
	ctx := t.Context()
	w.create(t, "a-down", nil)
	w.dir.answers["a-down"] = unreachable
	_, err := w.logins.Login(ctx, "alice", "pw")
	var failed *DirectoryError
	if !errors.As(err, &failed) || errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("an unreachable directory answered %v", err)
	}
	if !strings.Contains(w.log.String(), "connection=a-down") || strings.Contains(w.log.String(), password) {
		t.Fatalf("log: %s", w.log)
	}
	// Another connection's refusal answers for the name.
	w.create(t, "b-up", nil)
	w.dir.answers["b-up"] = accepts("bob", "pw")
	if _, err := w.logins.Login(ctx, "alice", "pw"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatalf("a refusal beside an outage: %v", err)
	}
	if got, err := w.logins.Login(ctx, "bob", "pw"); err != nil || !got.Fresh {
		t.Fatalf("a connection beside an outage: %+v, %v", got, err)
	}
}

func TestUnitLoginsStoreFailures(t *testing.T) {
	ctx := t.Context()
	w := newLoginWorld(t)
	w.create(t, "corp", nil)
	for _, op := range []string{"list"} {
		svc := w.svc
		svc.Configs = failingConfigs{ConfigStore: w.svc.Configs, op: op}
		l := NewLogins(svc, w.dir, LoginOptions{})
		if _, err := l.Login(ctx, "alice", "pw"); !errors.Is(err, errStore) {
			t.Fatalf("Login with a failing %s: %v", op, err)
		}
		if _, err := l.Current(ctx, "alice", [32]byte{}); !errors.Is(err, errStore) {
			t.Fatalf("Current with a failing %s: %v", op, err)
		}
	}
	noTx := w.svc
	noTx.Tx = nil
	if _, err := NewLogins(noTx, w.dir, LoginOptions{}).Login(ctx, "alice", "pw"); !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("Login without a snapshot: %v", err)
	}
}

func TestUnitTestConnection(t *testing.T) {
	w := newLoginWorld(t)
	ctx := t.Context()
	w.create(t, "corp", nil)
	stored := searchChange("corp")
	stored.ManagerPassword = nil
	for _, tc := range []struct {
		name     string
		change   accessapi.LDAPChange
		password string
		key      string
		invalid  bool
	}{
		{name: "stored password for the same server and manager", change: stored, password: password, key: "corp"},
		{name: "sent password wins", change: searchChange("corp"), password: password, key: "corp"},
		{name: "another server gets no stored password", change: func() accessapi.LDAPChange {
			ch := stored
			ch.URL = ptr("ldap://elsewhere.example.com/dc=example,dc=com")
			return ch
		}(), key: "corp"},
		{name: "another manager gets no stored password", change: func() accessapi.LDAPChange {
			ch := stored
			ch.ManagerDN = ptr("cn=other,dc=example,dc=com")
			return ch
		}(), key: "corp"},
		{name: "no key", change: func() accessapi.LDAPChange {
			ch := stored
			ch.Key = ""
			return ch
		}(), key: testKey},
		{name: "invalid", change: accessapi.LDAPChange{Key: "x", URL: ptr("http://h")}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := w.svc.TestConnection(ctx, tc.change)
			var refusal *accessapi.LDAPInvalid
			if tc.invalid {
				if !errors.As(err, &refusal) {
					t.Fatalf("TestConnection = %v, want a refusal", err)
				}
				return
			}
			if err != nil || c.Key != tc.key || c.ManagerPassword.Reveal() != tc.password {
				t.Fatalf("TestConnection = %+v (%q), %v", c, c.ManagerPassword.Reveal(), err)
			}
		})
	}
	svc := w.svc
	svc.Configs = failingConfigs{ConfigStore: w.svc.Configs, op: "get"}
	if _, err := svc.TestConnection(ctx, stored); !errors.Is(err, errStore) {
		t.Fatalf("TestConnection with a failing store: %v", err)
	}
	if SecretOf("x").Reveal() != "x" {
		t.Fatal("SecretOf")
	}
}
