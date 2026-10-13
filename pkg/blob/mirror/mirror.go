// Package mirror is a blob store that keeps each blob on several member
// stores at once (decision 0028): different clouds, regions or accounts, any
// mix of the fs, s3, gcs and azure backends.
//
// Writes go to Redundancy members in parallel and succeed once WriteQuorum of
// them hold the blob; members still committing get MemberTimeout to finish
// in the background, and every member's outcome is recorded in the replica
// index (store.ReplicaIndex) as present or pending, so the repair worker can
// fill missing copies later. Reads use the index to go straight to a member
// holding the blob, in read order: this instance's zone first, then by
// observed latency, members whose breaker is open last. A read that fails
// part-way reopens on the next holder at the same offset, which is safe
// because blobs are immutable.
//
// This is Artifactory's sharding provider (redundancy, lenientLimit,
// zone-aware reads) with three differences: members may be any backend in
// any cloud, an index records which members hold which blob instead of every
// read probing every shard, and surplus copies are never deleted
// automatically.
package mirror

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Sentinel errors. Test with errors.Is.
var (
	// ErrConfig reports an unusable configuration.
	ErrConfig = errors.New("blob/mirror: invalid configuration")
	// ErrQuorum reports a write that fewer than WriteQuorum members accepted.
	ErrQuorum = errors.New("blob/mirror: write quorum not met")
)

// Defaults for the zero Options fields.
const (
	// DefaultMemberTimeout is how long members still committing after the
	// quorum may finish before their copy is left to the repair worker.
	DefaultMemberTimeout = 30 * time.Second
	// DefaultChunkSize bounds the buffer one upload hands to every member at
	// once, so a mirrored upload holds one chunk in memory, not one per
	// member.
	DefaultChunkSize = 8 << 20
	// DefaultBreakerThreshold is the consecutive failures that open a
	// member's breaker.
	DefaultBreakerThreshold = 5
	// DefaultBreakerCooldown is how long an open breaker stays open before a
	// call is let through again; Artifactory's state-aware providers re-check
	// a failed store after the same 15 seconds (checkPeriod).
	DefaultBreakerCooldown = 15 * time.Second
)

// memberName restricts member names to what configuration keys, environment
// variable names and replica records can carry unchanged.
var memberName = regexp.MustCompile(`^[a-z0-9_]+$`)

// Member is one store of the mirror.
type Member struct {
	// Name identifies the member in the replica index; it must stay stable.
	Name string
	// Zone places the member, for example "aws-eu-west-1"; members in the
	// instance's zone are read and written first.
	Zone string
	// Store is the member's blob store.
	Store blob.Store
	// WriteEnabled lets the member receive new copies. A member with it
	// false is drained: it serves reads but gets nothing new.
	WriteEnabled bool
}

// Options configures New.
type Options struct {
	Members []Member
	// Redundancy is how many members keep each blob; zero means every
	// member (a full mirror).
	Redundancy int
	// WriteQuorum is how many members must store a blob before an upload
	// succeeds. Zero means Redundancy-1 (at least 1): by default an upload
	// still succeeds with one member down, which is the outage the mirror
	// exists to ride out, and the missing copy is repaired later.
	WriteQuorum int
	// Zone is this instance's zone.
	Zone string
	// MemberTimeout bounds how long members may finish after the quorum;
	// zero means DefaultMemberTimeout.
	MemberTimeout time.Duration
	// ChunkSize is the fan-out buffer; zero means DefaultChunkSize.
	ChunkSize int64
	// Sessions keeps the mirror's own upload sessions (the member upload
	// IDs). Members keep their own sessions.
	Sessions session.Store
	// Replicas records which members hold which blobs.
	Replicas store.ReplicaIndex
	// BreakerThreshold and BreakerCooldown tune each member's breaker; zero
	// means the defaults.
	BreakerThreshold int
	BreakerCooldown  time.Duration
	// Now supplies the time for the breaker, latency and replica records;
	// nil means time.Now.
	Now func() time.Time
}

// Store is a blob.Store over several member stores.
type Store struct {
	members       []*member
	byName        map[string]*member
	redundancy    int
	quorum        int
	zone          string
	memberTimeout time.Duration
	chunk         int64
	sessions      session.Store
	replicas      store.ReplicaIndex
	threshold     int
	cooldown      time.Duration
	now           func() time.Time
	// next rotates placement among members outside the instance's zone.
	next atomic.Uint64
	// inflight counts background commits per digest, so Delete waits for
	// them: a member finishing a commit after the delete would bring the
	// blob back.
	mu       sync.Mutex
	inflight map[string]*flight
}

// flight counts the background commits of one digest. An entry is removed
// when its count reaches zero, so a WaitGroup is never reused.
type flight struct {
	n  int
	wg sync.WaitGroup
}

// track registers a background commit of sha and returns its done func.
func (s *Store) track(sha string) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.inflight[sha]
	if f == nil {
		f = &flight{}
		s.inflight[sha] = f
	}
	f.n++
	f.wg.Add(1)
	return func() {
		s.mu.Lock()
		f.n--
		if f.n == 0 {
			delete(s.inflight, sha)
		}
		s.mu.Unlock()
		f.wg.Done()
	}
}

// settle waits until no background commit of sha is running, or ctx ends.
func (s *Store) settle(ctx context.Context, sha string) error {
	s.mu.Lock()
	f := s.inflight[sha]
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("blob/mirror: waiting for commits of %s: %w", sha, ctx.Err())
	}
}

// WaitIdle waits until the background commits running when it is called have
// finished and recorded their outcome, or ctx ends. A server calls it on
// shutdown so a member finishing after the quorum is recorded rather than
// left pending for no reason; a test calls it before looking at the members.
func (s *Store) WaitIdle(ctx context.Context) error {
	s.mu.Lock()
	shas := slices.Collect(maps.Keys(s.inflight))
	s.mu.Unlock()
	for _, sha := range shas {
		if err := s.settle(ctx, sha); err != nil {
			return err
		}
	}
	return nil
}

var (
	_ blob.Store     = (*Store)(nil)
	_ blob.Presigner = (*Store)(nil)
)

// member is a Member with its health.
type member struct {
	Member
	idx    int
	health health
}

// New returns a mirror over o.Members.
func New(o Options) (*Store, error) {
	if len(o.Members) == 0 {
		return nil, fmt.Errorf("%w: no members", ErrConfig)
	}
	s := &Store{
		byName: map[string]*member{}, inflight: map[string]*flight{},
		redundancy: o.Redundancy, quorum: o.WriteQuorum,
		zone: o.Zone, memberTimeout: cmp.Or(o.MemberTimeout, DefaultMemberTimeout),
		chunk: cmp.Or(o.ChunkSize, DefaultChunkSize), sessions: o.Sessions, replicas: o.Replicas,
		threshold: cmp.Or(o.BreakerThreshold, DefaultBreakerThreshold),
		cooldown:  cmp.Or(o.BreakerCooldown, DefaultBreakerCooldown),
		now:       o.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	writable := 0
	for i, m := range o.Members {
		switch {
		case !memberName.MatchString(m.Name):
			return nil, fmt.Errorf(
				"%w: member name %q must match %s",
				ErrConfig,
				m.Name,
				memberName,
			)
		case s.byName[m.Name] != nil:
			return nil, fmt.Errorf("%w: member %q given twice", ErrConfig, m.Name)
		case m.Store == nil:
			return nil, fmt.Errorf("%w: member %q has no store", ErrConfig, m.Name)
		}
		mm := &member{Member: m, idx: i}
		s.members = append(s.members, mm)
		s.byName[m.Name] = mm
		if m.WriteEnabled {
			writable++
		}
	}
	if s.redundancy == 0 {
		s.redundancy = len(s.members)
	}
	if s.quorum == 0 {
		s.quorum = max(1, s.redundancy-1)
	}
	switch {
	case s.redundancy < 1 || s.redundancy > len(s.members):
		return nil, fmt.Errorf("%w: redundancy %d must be between 1 and the %d members",
			ErrConfig, s.redundancy, len(s.members))
	case s.quorum < 1 || s.quorum > s.redundancy:
		return nil, fmt.Errorf("%w: write quorum %d must be between 1 and redundancy %d",
			ErrConfig, s.quorum, s.redundancy)
	case writable < s.quorum:
		return nil, fmt.Errorf("%w: %d write-enabled members cannot meet write quorum %d",
			ErrConfig, writable, s.quorum)
	case s.sessions == nil || s.replicas == nil:
		return nil, fmt.Errorf("%w: session store and replica index are required", ErrConfig)
	case s.chunk < 1 || s.memberTimeout < 0 || s.threshold < 1 || s.cooldown < 0:
		return nil, fmt.Errorf(
			"%w: chunk size, timeouts and breaker settings must be positive",
			ErrConfig,
		)
	}
	return s, nil
}

// health is a member's breaker and observed latency. The breaker opens after
// threshold consecutive failures and stays open for the cooldown; after
// that, calls go through again and the next failure reopens it at once while
// a success closes it, which lets one call through at a time as a probe.
type health struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	latency   time.Duration
	samples   int
}

func (h *health) open(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return now.Before(h.openUntil)
}

func (h *health) success(took time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures, h.openUntil = 0, time.Time{}
	// An exponentially weighted average, so one slow call does not reorder
	// reads but a member that stays slow drops behind.
	if h.samples == 0 {
		h.latency = took
	} else {
		h.latency = (4*h.latency + took) / 5
	}
	h.samples++
}

func (h *health) failure(now time.Time, threshold int, cooldown time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures++
	if h.failures >= threshold {
		h.openUntil = now.Add(cooldown)
	}
}

func (h *health) avg() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latency
}

// observe records a member call's outcome. Answers that are not the
// member's fault (a missing blob, a cancelled request, a client's wrong
// digest, a finished session) leave its health alone.
func (s *Store) observe(m *member, start time.Time, err error) {
	switch {
	case err == nil:
		m.health.success(s.now().Sub(start))
	case errors.Is(err, blob.ErrNotFound), errors.Is(err, context.Canceled),
		errors.Is(err, blob.ErrDigestMismatch), errors.Is(err, blob.ErrUploadUnknown),
		errors.Is(err, blob.ErrInvalidDigest):
	default:
		m.health.failure(s.now(), s.threshold, s.cooldown)
	}
}

// readOrder sorts members for reading: closed breakers first, then this
// instance's zone, then lower observed latency, then configuration order.
func (s *Store) readOrder(ms []*member) []*member {
	now := s.now()
	out := slices.Clone(ms)
	slices.SortStableFunc(out, func(a, b *member) int {
		return cmp.Or(
			boolOrder(a.health.open(now), b.health.open(now)),
			boolOrder(!s.inZone(a), !s.inZone(b)),
			cmp.Compare(a.health.avg(), b.health.avg()),
			cmp.Compare(a.idx, b.idx),
		)
	})
	return out
}

func (s *Store) inZone(m *member) bool { return s.zone != "" && m.Zone == s.zone }

// boolOrder sorts false before true.
func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// place chooses the members a new upload writes to: write-enabled members
// with a closed breaker, this instance's zone first and the rest in rotation,
// up to Redundancy. When fewer healthy members exist, the unhealthy ones that
// would have made up Redundancy are returned as deferred so the repair
// worker fills their copies later.
func (s *Store) place() (targets, deferred []*member) {
	var zone, rest []*member
	for _, m := range s.members {
		if !m.WriteEnabled {
			continue
		}
		if s.inZone(m) {
			zone = append(zone, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(rest) > 0 {
		k := int(
			s.next.Add(1)-1,
		) % len(
			rest,
		) // #nosec G115 -- a rotation counter; wrapping is harmless
		rest = slices.Concat(rest[k:], rest[:k])
	}
	now := s.now()
	var unhealthy []*member
	for _, m := range slices.Concat(zone, rest) {
		switch {
		case m.health.open(now):
			unhealthy = append(unhealthy, m)
		case len(targets) < s.redundancy:
			targets = append(targets, m)
		}
	}
	if short := s.redundancy - len(targets); short > 0 {
		deferred = unhealthy[:min(short, len(unhealthy))]
	}
	return targets, deferred
}

// parallel runs f for every index at once and returns the results in order.
// Each call gets the caller's context unchanged: one member failing must not
// cancel the others, which is why this is not an errgroup.
func parallel[T any](n int, f func(i int) (T, error)) ([]T, []error) {
	out := make([]T, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { out[i], errs[i] = f(i) })
	}
	wg.Wait()
	return out, errs
}
