package auth

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Temporary login suspension, the second of Artifactory 7.x's two defences
// against guessing passwords beside the user lock ("User Lock and Login
// Suspension", docs.jfrog.com security configuration). Failed Basic logins
// suspend the user name for a delay that doubles from
// base_login_delay_millis up to max_login_delay_millis; while it lasts every
// Basic login with that name answers 403, the right password included, and
// is ignored rather than counted, so a stream of wrong passwords cannot
// stretch one suspension past the maximum. A login whose password verifies
// once the suspension is over forgets the failures.
//
// The state is per process, as JFrog documents it per node of a cluster: a
// node that restarts forgets it, and the persistent user lock is what bounds
// the guesses in total.

// ErrLoginSuspended reports a Basic login refused because its user name is
// suspended. The error also carries the 403 the client gets.
var ErrLoginSuspended = errors.New("auth: login temporarily suspended")

const (
	// suspensionsMax bounds the user names tracked, as Artifactory's
	// security.max.users.toTrack (10000) bounds its own. Names that do not
	// exist are tracked too, so that the 403 does not tell which do.
	suspensionsMax = 10_000
	// suspensionMemory is how long failures count after the last one. JFrog
	// documents no window; this is the lifetime of Artifactory 6.x's
	// tracking caches.
	suspensionMemory = 24 * time.Hour
	// maxDelayMillis keeps a configured delay within time.Duration.
	maxDelayMillis = int64(1<<63-1) / int64(time.Millisecond)
)

// suspendedFor is the 403 of a login suspended for d more, in the wording of
// the REST body Artifactory 7.x answers (RTFACT-29934 on 7.61.1,
// RTFACT-26575 on 7.27.6). The KB's "recurrent request failures ...
// second(s)" is the log line, not the body. "seconds" stays plural for one
// second too, as RTFACT-26575's "try again in 1 seconds" shows.
func suspendedFor(d time.Duration) error {
	secs := int64((d + time.Second - 1) / time.Second)
	return fmt.Errorf("%w: %w", ErrLoginSuspended, apierror.New(http.StatusForbidden, fmt.Sprintf(
		"This request is blocked due to recurrent login failures, please try again in %d seconds",
		secs,
	)))
}

// suspensionOn reports whether s suspends anything at all. A zero delay
// turns suspension off, as Artifactory's maxLoginBlockDelay of 0 does.
func suspensionOn(s types.AccessLockSettings) bool {
	return s.ClientLoginSuspensionEnabled && s.BaseLoginDelayMillis > 0 && s.MaxLoginDelayMillis > 0
}

// suspensionDelay is the suspension that the n-th consecutive failure
// starts: none before max_login_delay_incorrect_attempts failures, then the
// base delay, doubled for each failure past that and capped at the maximum
// (Artifactory 6.x's loginBlockDelay << (attempts - allowed)). A threshold
// below 1 suspends from the first failure.
func suspensionDelay(n int, s types.AccessLockSettings) time.Duration {
	if !suspensionOn(s) {
		return 0
	}
	threshold := max(s.MaxLoginDelayIncorrectAttempts, 1)
	if n < threshold {
		return 0
	}
	limit := min(s.MaxLoginDelayMillis, maxDelayMillis)
	ms := min(s.BaseLoginDelayMillis, limit)
	for range n - threshold {
		if ms > limit/2 {
			ms = limit
			break
		}
		ms *= 2
	}
	return time.Duration(ms) * time.Millisecond
}

// suspension is one user name's failures.
type suspension struct {
	failures int
	last     time.Time
	until    time.Time
}

func (s suspension) stale(now time.Time) bool {
	return !now.Before(s.until) && now.Sub(s.last) >= suspensionMemory
}

// suspensions tracks failed logins by user name.
type suspensions struct {
	mu sync.Mutex
	m  map[string]suspension
}

// remaining is how long name stays suspended at now; zero when it is not.
func (s *suspensions) remaining(name string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[name]
	if !ok {
		return 0
	}
	if e.stale(now) {
		delete(s.m, name)
		return 0
	}
	return max(e.until.Sub(now), 0)
}

// fail counts a failed login of name at now and starts the suspension the
// count calls for. A failure that lands during a suspension, from a request
// already past the check when the suspension began, is not counted: a burst
// of concurrent wrong passwords starts one suspension rather than
// stretching it.
func (s *suspensions) fail(name string, now time.Time, set types.AccessLockSettings) {
	if !suspensionOn(set) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[name]
	switch {
	case ok && now.Before(e.until):
		return
	case ok && e.stale(now):
		e = suspension{}
	case !ok:
		s.makeRoom(now)
	}
	e.failures++
	e.last = now
	e.until = now.Add(suspensionDelay(e.failures, set))
	s.m[name] = e
}

// forget drops name's failures, after a login whose password verified.
func (s *suspensions) forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
}

// makeRoom keeps the map within suspensionsMax: stale entries go first,
// then names that are not suspended now, then any. Dropping an entry only
// forgets failures, so a flood of names can shorten suspensions but never
// extend one.
func (s *suspensions) makeRoom(now time.Time) {
	if s.m == nil {
		s.m = map[string]suspension{}
	}
	for _, drop := range []func(suspension) bool{
		func(e suspension) bool { return e.stale(now) },
		func(e suspension) bool { return !now.Before(e.until) },
		func(suspension) bool { return true },
	} {
		for name, e := range s.m {
			if len(s.m) < suspensionsMax {
				return
			}
			if drop(e) {
				delete(s.m, name)
			}
		}
	}
}
