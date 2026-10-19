package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestSuspensionDelay(t *testing.T) {
	on := types.DefaultAccessLockSettings()
	huge := on
	huge.MaxLoginDelayMillis = 1<<63 - 1
	for name, c := range map[string]struct {
		n    int
		set  types.AccessLockSettings
		want time.Duration
	}{
		// 7.x's defaults: suspended from the second failure, from 1 s up
		// to 60 s.
		"first failure":   {1, on, 0},
		"threshold":       {2, on, time.Second},
		"doubled":         {3, on, 2 * time.Second},
		"doubled again":   {7, on, 32 * time.Second},
		"capped":          {8, on, time.Minute},
		"capped far out":  {1000, on, time.Minute},
		"no threshold":    {1, with(on, func(s *types.AccessLockSettings) { s.MaxLoginDelayIncorrectAttempts = 0 }), time.Second},
		"switched off":    {5, with(on, func(s *types.AccessLockSettings) { s.ClientLoginSuspensionEnabled = false }), 0},
		"no base delay":   {5, with(on, func(s *types.AccessLockSettings) { s.BaseLoginDelayMillis = 0 }), 0},
		"no max delay":    {5, with(on, func(s *types.AccessLockSettings) { s.MaxLoginDelayMillis = 0 }), 0},
		"base above max":  {2, with(on, func(s *types.AccessLockSettings) { s.BaseLoginDelayMillis = 90_000 }), time.Minute},
		"near the limit":  {35, huge, time.Duration(1000<<33) * time.Millisecond},
		"overflow halted": {36, huge, time.Duration(maxDelayMillis) * time.Millisecond},
		"far past it":     {200, huge, time.Duration(maxDelayMillis) * time.Millisecond},
	} {
		if got := suspensionDelay(c.n, c.set); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func with(s types.AccessLockSettings, f func(*types.AccessLockSettings)) types.AccessLockSettings {
	f(&s)
	return s
}

// suspendedWait returns the seconds a suspended login's 403 tells the
// client to wait, failing the test for any other answer.
func suspendedWait(t *testing.T, err error) int {
	t.Helper()
	var e *apierror.Error
	if !errors.Is(err, ErrLoginSuspended) || !errors.As(err, &e) ||
		e.Status != http.StatusForbidden {
		t.Fatalf("want a suspended login's 403, got %v", err)
	}
	var secs int
	if _, err := fmt.Sscanf(
		e.Message,
		"This request is blocked due to recurrent request failures, please try again in %d second(s)",
		&secs,
	); err != nil {
		t.Fatalf("message %q: %v", e.Message, err)
	}
	return secs
}

func TestSuspensionThroughBasic(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev", Status: types.UserEnabled}, "pw")
	// The lock policy is on but far off, to count the failures it sees.
	if err := db.Settings().Put(ctx, types.SettingUserLockPolicy,
		types.UserLockPolicy{Enabled: true, LoginAttempts: 100}); err != nil {
		t.Fatal(err)
	}
	a, c := newAuth(t, db, Options{})
	wrong := func(name string) {
		t.Helper()
		if _, err := a.Basic(ctx, name, "wrong"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("%s, wrong password: %v", name, err)
		}
	}

	// The first failure is allowed; the second suspends for a second, in
	// which even the right password answers 403.
	wrong("dev")
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("after one failure: %v", err)
	}
	wrong("dev")
	wrong("dev")
	if got := suspendedWait(t, ezBasic(a, "dev", "pw")); got != 1 {
		t.Fatalf("waits %d s, want 1", got)
	}
	// Attempts while suspended are ignored: they neither stretch the
	// suspension nor count toward the next.
	c.t = c.t.Add(500 * time.Millisecond)
	for range 20 {
		suspendedWait(t, ezBasic(a, "dev", "wrong"))
	}
	c.t = c.t.Add(500 * time.Millisecond)
	wrong("dev")
	if got := suspendedWait(t, ezBasic(a, "dev", "pw")); got != 2 {
		t.Fatalf("the third failure suspends for %d s, want 2", got)
	}
	// Only the failures whose password was checked count toward the lock:
	// the three since the last login, not the twenty refused unchecked.
	if u, _ := db.Users().Get(ctx, "dev"); u.FailedLogins != 3 {
		t.Fatalf("%d failures counted toward the lock, want 3", u.FailedLogins)
	}
	// Once the suspension is over the right password signs in and clears
	// the failures: the next lone failure suspends nothing.
	c.t = c.t.Add(2 * time.Second)
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("after the suspension: %v", err)
	}
	wrong("dev")
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("the failures were not cleared: %v", err)
	}

	// A name that does not exist is suspended alike, so the 403 tells
	// nothing about which names do.
	wrong("ghost")
	wrong("ghost")
	if got := suspendedWait(t, ezBasic(a, "ghost", "pw")); got != 1 {
		t.Fatalf("ghost waits %d s, want 1", got)
	}
	// Failures are forgotten a day after the last one.
	c.t = c.t.Add(suspensionMemory)
	wrong("ghost")
	if _, err := a.Basic(ctx, "ghost", "pw"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a day later, one failure suspended ghost: %v", err)
	}
}

// ezBasic is a.Basic's error.
func ezBasic(a *Authenticator, name, password string) error {
	_, err := a.Basic(context.Background(), name, password)
	return err
}

// TestSuspensionBeforeHashing checks a suspended name is refused before any
// password is hashed: with every hashing slot taken, the login still
// answers at once.
func TestSuspensionBeforeHashing(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev", Status: types.UserEnabled}, "pw")
	a, _ := newAuth(t, db, Options{Concurrency: 1})
	for range 2 {
		_, _ = a.Basic(ctx, "dev", "wrong")
	}
	a.slots <- struct{}{}
	defer func() { <-a.slots }()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, pw := range []string{"pw", "wrong"} {
		if _, err := a.Basic(cancelled, "dev", pw); !errors.Is(err, ErrLoginSuspended) {
			t.Fatalf("%s while suspended: %v", pw, err)
		}
	}
}

func TestSuspensionSettings(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	addUser(t, db, types.User{Name: "dev", Status: types.UserEnabled}, "pw")
	a, c := newAuth(t, db, Options{})
	put := func(s types.AccessLockSettings) {
		t.Helper()
		if err := db.Settings().Put(ctx, types.SettingAccessLockSettings, s); err != nil {
			t.Fatal(err)
		}
		a.ForgetPolicies()
	}

	// Stored settings apply: here from the first failure, 5 s at once.
	put(with(types.DefaultAccessLockSettings(), func(s *types.AccessLockSettings) {
		s.MaxLoginDelayIncorrectAttempts, s.BaseLoginDelayMillis = 1, 5000
	}))
	_, _ = a.Basic(ctx, "dev", "wrong")
	if got := suspendedWait(t, ezBasic(a, "dev", "pw")); got != 5 {
		t.Fatalf("waits %d s, want 5", got)
	}
	// Switching suspension off lifts one in progress and starts no other.
	put(with(types.DefaultAccessLockSettings(), func(s *types.AccessLockSettings) {
		s.ClientLoginSuspensionEnabled = false
	}))
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("suspension off: %v", err)
	}
	for range 5 {
		_, _ = a.Basic(ctx, "dev", "wrong")
	}
	if _, err := a.Basic(ctx, "dev", "pw"); err != nil {
		t.Fatalf("suspension off, after failures: %v", err)
	}
	// Settings that cannot be read suspend nothing; the lock and the
	// password checks go on as before.
	broken, err := NewAuthenticator(db.Users(), failingSettings{db.Settings()},
		Options{Now: func() time.Time { return c.t }})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := broken.Basic(ctx, "dev", "wrong"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("unreadable settings: %v", err)
		}
	}
}

func TestSuspensionsBounded(t *testing.T) {
	set := with(types.DefaultAccessLockSettings(), func(s *types.AccessLockSettings) {
		s.MaxLoginDelayIncorrectAttempts = 1
	})
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	var s suspensions
	fill := func(at time.Time) {
		t.Helper()
		for i := range suspensionsMax {
			s.fail(fmt.Sprintf("u%d", i), at, set)
		}
	}
	fill(t0)
	if len(s.m) != suspensionsMax {
		t.Fatalf("%d names tracked, want %d", len(s.m), suspensionsMax)
	}
	// Full of names suspended now: one of them makes way.
	s.fail("late", t0, set)
	if len(s.m) != suspensionsMax || s.remaining("late", t0) != time.Second {
		t.Fatalf("all suspended: %d names, late waits %v", len(s.m), s.remaining("late", t0))
	}
	// Once the suspensions are over, a name no longer suspended makes way
	// before any that still is.
	t1 := t0.Add(time.Minute)
	s.fail("later", t1, set)
	s.fail("latest", t1, set)
	if len(s.m) != suspensionsMax || s.remaining("later", t1) != time.Second {
		t.Fatalf("a suspended name was dropped: %d names", len(s.m))
	}
	// Stale names go first.
	fill(t1)
	t2 := t1.Add(suspensionMemory)
	s.fail("fresh", t2, set)
	if len(s.m) != suspensionsMax || s.remaining("fresh", t2) != time.Second {
		t.Fatalf("stale names: %d names, fresh waits %v", len(s.m), s.remaining("fresh", t2))
	}
	s.forget("fresh")
	if s.remaining("fresh", t2) != 0 {
		t.Fatal("fresh was not forgotten")
	}
}

// TestSuspensionsForgetStale checks a name's failures stop counting a day
// after the last one: the next failure starts over.
func TestSuspensionsForgetStale(t *testing.T) {
	set := types.DefaultAccessLockSettings()
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	var s suspensions
	s.fail("dev", t0, set)
	s.fail("dev", t0, set)
	later := t0.Add(suspensionMemory)
	s.fail("dev", later, set)
	if got := s.remaining("dev", later); got != 0 {
		t.Fatalf("one failure after a stale run suspends for %v", got)
	}
	s.fail("dev", later, set)
	if got := s.remaining("dev", later); got != time.Second {
		t.Fatalf("the second fresh failure suspends for %v, want 1s", got)
	}
}
