// Package work runs the persistent work queue (decision 0033): a pool of
// workers that lease due items, run the handler registered for each item's
// kind, and complete, retry or park it. Work is asked for with Enqueue
// inside the transaction of the change that needs it; readers that must see
// the work done call EnsureFresh first.
package work

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Defaults for Options left zero.
const (
	DefaultWorkers     = 4
	DefaultLease       = 2 * time.Minute
	DefaultMaxAttempts = 10
	DefaultPoll        = time.Second
	// maxBackoff caps the wait between retries.
	maxBackoff = 10 * time.Minute
	// settle bounds the store calls that end a run, which must happen even
	// when the run's own context has been cancelled.
	settle = 10 * time.Second
)

var (
	// ErrParked reports that the work a reader waits for keeps failing and
	// is parked until it is enqueued again.
	ErrParked = errors.New("work: parked")
	// ErrNoHandler reports a kind with no registered handler.
	ErrNoHandler = errors.New("work: no handler")
	// ErrOptions reports options New cannot use.
	ErrOptions = errors.New("work: invalid options")
	// ErrDuplicateHandler reports a second handler for one kind.
	ErrDuplicateHandler = errors.New("work: kind already has a handler")
	// ErrPanic reports a handler that panicked.
	ErrPanic = errors.New("work: handler panicked")
)

// Handler does the work of one item. It must be idempotent: an item can run
// more than once, for example when a worker stops after the work but before
// recording it. A cancelled ctx means the queue is stopping or the lease was
// lost; the handler should return promptly.
type Handler func(ctx context.Context, item types.WorkItem) error

// Options configures a Queue.
type Options struct {
	// Workers is how many items run at once on this node.
	Workers int
	// Lease is how long a run holds its item before another worker may
	// take it over; a running item's lease is extended every half lease.
	Lease time.Duration
	// MaxAttempts is how many runs an item gets before it is parked.
	MaxAttempts int
	// Poll is how long an idle worker waits before asking again, when no
	// enqueue on this node wakes it sooner.
	Poll time.Duration
	// Holder names this node's leases; empty uses the host name, the
	// process id and a random suffix.
	Holder string
	// Backoff is the wait before an item's next run after its attempt'th
	// failed; nil doubles from a second up to ten minutes.
	Backoff func(attempt int) time.Duration
	Now     func() time.Time
	Log     *slog.Logger
	// Meters exports the queue's metrics (decision 0016); nil exports none.
	Meters metric.MeterProvider
}

// Queue runs the persistent work queue for one node.
type Queue struct {
	st       store.Store
	q        store.WorkQueue
	o        Options
	mu       sync.Mutex
	handlers map[string]Handler
	// every is the interval of each periodic kind (HandleEvery).
	every   map[string]time.Duration
	wake    chan struct{}
	running atomic.Int64
	runs    metric.Int64Counter
}

// New returns a queue over st; register handlers with Handle before Run.
func New(st store.Store, o Options) (*Queue, error) {
	if o.Workers == 0 {
		o.Workers = DefaultWorkers
	}
	if o.Lease == 0 {
		o.Lease = DefaultLease
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.Poll == 0 {
		o.Poll = DefaultPoll
	}
	if o.Workers < 0 || o.Lease < 0 || o.MaxAttempts < 0 || o.Poll < 0 {
		return nil, fmt.Errorf(
			"%w: workers, lease, attempts and poll must not be negative",
			ErrOptions,
		)
	}
	if o.Holder == "" {
		o.Holder = holderName()
	}
	if o.Backoff == nil {
		o.Backoff = Backoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Meters == nil {
		o.Meters = noop.NewMeterProvider()
	}
	q := &Queue{
		st: st, q: st.WorkQueue(), o: o,
		handlers: map[string]Handler{}, every: map[string]time.Duration{},
		wake: make(chan struct{}, 1),
	}
	if err := q.instrument(); err != nil {
		return nil, err
	}
	return q, nil
}

// Backoff doubles from one second after the first failure up to ten
// minutes.
func Backoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

func holderName() string {
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b))
}

// Handle registers h for kind. Register every handler before Run: Run
// leases only the kinds registered when it starts. A second handler for one
// kind is an error.
func (q *Queue) Handle(kind string, h Handler) error {
	return q.handle(kind, 0, h)
}

// HandleEvery registers h for kind as periodic work, for work that
// Artifactory does on a timer rather than as it is asked for, such as its
// snapshot cleanup every five minutes. An item of kind is not run as soon
// as it is due: it waits in the queue, so a restart keeps it, for the next
// tick of a timer every interval on each node, which runs every item of
// kind then due (RunDue). EnsureFresh still runs one at once. Register it
// before Run, as Handle.
func (q *Queue) HandleEvery(kind string, every time.Duration, h Handler) error {
	if every <= 0 {
		return fmt.Errorf("%w: periodic kind %q needs an interval above zero", ErrOptions, kind)
	}
	return q.handle(kind, every, h)
}

func (q *Queue) handle(kind string, every time.Duration, h Handler) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, dup := q.handlers[kind]; dup {
		return fmt.Errorf("%w: %q", ErrDuplicateHandler, kind)
	}
	q.handlers[kind] = h
	if every > 0 {
		q.every[kind] = every
	}
	return nil
}

func (q *Queue) handler(kind string) (Handler, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	h, ok := q.handlers[kind]
	return h, ok
}

// schedule splits the registered kinds into those the workers lease as
// they fall due and the periodic ones, with their intervals.
func (q *Queue) schedule() (polled []string, periodic map[string]time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	periodic = maps.Clone(q.every)
	for k := range q.handlers {
		if _, ok := periodic[k]; !ok {
			polled = append(polled, k)
		}
	}
	slices.Sort(polled)
	return polled, periodic
}

// Enqueue asks for kind's work on key. Call it with the context of the
// transaction that makes the change needing the work, so the work is asked
// for exactly when the change commits; this node's workers are woken once
// it has.
func (q *Queue) Enqueue(ctx context.Context, kind, key string, payload []byte) error {
	if err := q.q.Enqueue(ctx, kind, key, payload, q.o.Now()); err != nil {
		return fmt.Errorf("work: enqueue %s/%s: %w", kind, key, err)
	}
	q.st.AfterCommit(ctx, q.signal)
	return nil
}

// signal wakes one idle worker, if any.
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Run leases and runs items until ctx ends, then hands back the leases of
// runs it cuts short and returns once every run has settled. Periodic
// kinds run on their own timers, outside the workers.
func (q *Queue) Run(ctx context.Context) error {
	polled, periodic := q.schedule()
	if len(polled) == 0 && len(periodic) == 0 {
		<-ctx.Done()
		return nil
	}
	var wg sync.WaitGroup
	if len(polled) > 0 {
		for range q.o.Workers {
			wg.Go(func() { q.worker(ctx, polled) })
		}
	}
	for kind, every := range periodic {
		wg.Go(func() { q.tick(ctx, kind, every) })
	}
	wg.Wait()
	return nil
}

// tick runs kind's due items every interval until ctx ends.
func (q *Queue) tick(ctx context.Context, kind string, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := q.RunDue(ctx, kind); err != nil && ctx.Err() == nil {
				q.o.Log.Warn("work queue periodic run failed", "kind", kind, "error", err)
			}
		}
	}
}

// RunDue runs every item of kind that is due now, one after another, and
// returns how many runs it made. A periodic kind's timer calls it; so may a
// caller that need not wait for the timer, such as a test. A run that fails
// is retried after its backoff, never in the same pass, and an item asked
// for again during its run waits for the next pass: it is due again from
// the end of its run, after every item that was due before it, so meeting
// it again means the pass has taken everything else.
func (q *Queue) RunDue(ctx context.Context, kind string) (int, error) {
	if _, ok := q.handler(kind); !ok {
		return 0, fmt.Errorf("%w for %q", ErrNoHandler, kind)
	}
	ran := map[string]bool{}
	kinds := []string{kind}
	for ctx.Err() == nil {
		now := q.o.Now()
		items, err := q.q.Lease(ctx, store.WorkLease{
			Kinds: kinds, Holder: q.o.Holder, Limit: 1, Until: now.Add(q.o.Lease), Now: now,
		})
		switch {
		case err != nil:
			return len(ran), fmt.Errorf("work: lease %s: %w", kind, err)
		case len(items) == 0:
			return len(ran), nil
		}
		it := items[0]
		if ran[it.Key] {
			// Handed back unrun, so the attempt is not counted.
			if err := q.q.Release(ctx, it.Kind, it.Key, it.Generation, q.o.Now()); err != nil {
				return len(ran), fmt.Errorf("work: release %s/%s: %w", kind, it.Key, err)
			}
			return len(ran), nil
		}
		ran[it.Key] = true
		_ = q.run(ctx, it)
	}
	return len(ran), nil
}

func (q *Queue) worker(ctx context.Context, kinds []string) {
	for ctx.Err() == nil {
		now := q.o.Now()
		items, err := q.q.Lease(ctx, store.WorkLease{
			Kinds: kinds, Holder: q.o.Holder, Limit: 1, Until: now.Add(q.o.Lease), Now: now,
		})
		if err != nil {
			if ctx.Err() == nil {
				q.o.Log.Warn("work queue lease failed", "error", err)
			}
			q.idle(ctx)
			continue
		}
		if len(items) == 0 {
			q.idle(ctx)
			continue
		}
		// More may be due: let another idle worker look too.
		q.signal()
		_ = q.run(ctx, items[0])
	}
}

// idle waits for a wake-up, the poll interval or the end of ctx.
func (q *Queue) idle(ctx context.Context) {
	t := time.NewTimer(q.o.Poll)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-q.wake:
	case <-t.C:
	}
}

// run runs a leased item's handler and records how it ended. It returns the
// handler's error.
func (q *Queue) run(ctx context.Context, it types.WorkItem) error {
	q.running.Add(1)
	defer q.running.Add(-1)
	h, ok := q.handler(it.Kind)
	if !ok {
		// Only registered kinds are leased; a reader's LeaseKey of another
		// kind is refused before it gets here.
		return fmt.Errorf("%w for %q", ErrNoHandler, it.Kind)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	beat := make(chan struct{})
	go q.heartbeat(runCtx, cancel, it, beat)
	err := call(runCtx, h, it)
	cancel()
	<-beat
	q.settle(ctx, it, err)
	return err
}

// heartbeat extends a run's lease every half lease until the run ends, and
// cancels the run when the lease is lost.
func (q *Queue) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	it types.WorkItem,
	done chan<- struct{},
) {
	defer close(done)
	t := time.NewTicker(q.o.Lease / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := q.o.Now()
			err := q.q.Extend(ctx, it.Kind, it.Key, it.Generation, now.Add(q.o.Lease), now)
			if errors.Is(err, store.ErrConflict) {
				q.o.Log.Warn("work queue lease lost", "kind", it.Kind, "key", it.Key)
				cancel()
				return
			}
			if err != nil && ctx.Err() == nil {
				q.o.Log.Warn("work queue lease extension failed", "kind", it.Kind, "error", err)
			}
		}
	}
}

// call runs h, turning a panic into an error so one bad item cannot stop a
// worker.
func call(ctx context.Context, h Handler, it types.WorkItem) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v\n%s", ErrPanic, r, debug.Stack())
		}
	}()
	return h(ctx, it)
}

// settle records how a run ended: done, retried, parked, or handed back when
// the queue is stopping.
func (q *Queue) settle(ctx context.Context, it types.WorkItem, runErr error) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settle)
	defer cancel()
	now := q.o.Now()
	var (
		outcome string
		err     error
	)
	switch {
	case runErr == nil:
		outcome, err = "completed", q.q.Complete(sctx, it.Kind, it.Key, it.Generation, now)
	case ctx.Err() != nil:
		outcome, err = "released", q.q.Release(sctx, it.Kind, it.Key, it.Generation, now)
	case it.Attempts >= q.o.MaxAttempts:
		outcome = "parked"
		err = q.q.Fail(sctx, it.Kind, it.Key, it.Generation, store.WorkFailure{
			Error: runErr.Error(), Park: true, RetryAt: now.Add(q.o.Backoff(it.Attempts)), Now: now,
		})
		q.o.Log.Error("work item parked after repeated failures",
			"kind", it.Kind, "key", it.Key, "attempts", it.Attempts, "error", runErr)
	default:
		outcome = "failed"
		err = q.q.Fail(sctx, it.Kind, it.Key, it.Generation, store.WorkFailure{
			Error: runErr.Error(), RetryAt: now.Add(q.o.Backoff(it.Attempts)), Now: now,
		})
		q.o.Log.Warn("work item failed; retrying",
			"kind", it.Kind, "key", it.Key, "attempt", it.Attempts, "error", runErr)
	}
	switch {
	case errors.Is(err, store.ErrConflict):
		// Another worker took the item over after this run's lease ran out;
		// its own run decides how the item ends.
		outcome = "lost"
	case err != nil:
		q.o.Log.Warn("work queue could not record a run", "kind", it.Kind, "error", err)
	}
	q.runs.Add(sctx, 1, metric.WithAttributes(
		attribute.String("kind", it.Kind), attribute.String("outcome", outcome)))
}

// EnsureFresh brings the work owed for kind and key up to date before the
// caller reads what it derives: it runs a waiting item now, in the caller's
// context, or waits for a run already in progress elsewhere. It returns nil
// when nothing is owed, the handler's error when the run it made fails, and
// ErrParked when the item is parked.
func (q *Queue) EnsureFresh(ctx context.Context, kind, key string) error {
	if _, ok := q.handler(kind); !ok {
		return fmt.Errorf("%w for %q", ErrNoHandler, kind)
	}
	// Most reads owe nothing, as every download of a Maven metadata file
	// asks: a plain read says so without the locking transaction a lease
	// takes, which on SQLite holds its one writer. Any other answer is left
	// to the lease.
	if _, err := q.q.Get(ctx, kind, key); errors.Is(err, store.ErrNotFound) {
		return nil
	}
	wait := 10 * time.Millisecond
	for {
		now := q.o.Now()
		it, took, err := q.q.LeaseKey(ctx, kind, key, store.WorkLease{
			Holder: q.o.Holder, Until: now.Add(q.o.Lease), Now: now,
		})
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("work: ensure %s/%s: %w", kind, key, err)
		case took:
			return q.run(ctx, it)
		case it.State == types.WorkParked:
			return fmt.Errorf("%w: %s/%s: %s", ErrParked, kind, key, it.LastError)
		}
		// Running elsewhere: wait for that run to end.
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("work: ensure %s/%s: %w", kind, key, ctx.Err())
		case <-t.C:
		}
		wait = min(wait*2, q.o.Poll)
	}
}

// WaitIdle returns once no item of a registered kind is pending or leased
// and no run is in progress on this node, for tests. Parked items do not
// count; an item that keeps failing keeps it waiting until ctx ends. A
// pending item of a periodic kind does not count either, since it waits
// for its timer by design; RunDue runs it.
func (q *Queue) WaitIdle(ctx context.Context) error {
	polled, periodic := q.schedule()
	for {
		busy := q.running.Load() > 0
		if !busy {
			stats, err := q.q.Stats(ctx)
			if err != nil {
				return fmt.Errorf("work: wait idle: %w", err)
			}
			for _, st := range stats {
				_, timed := periodic[st.Kind]
				switch {
				case slices.Contains(polled, st.Kind) && st.Pending+st.Leased > 0,
					timed && st.Leased > 0:
					busy = true
				}
			}
		}
		if !busy {
			return nil
		}
		t := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("work: wait idle: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// instrument registers the queue's metrics: items by kind and state, the
// age of the oldest pending item, and runs by outcome. Kinds are code
// constants, so the labels stay bounded (decision 0016).
func (q *Queue) instrument() error {
	m := q.o.Meters.Meter("github.com/weaveplatform/artifactweave/internal/service/work")
	var err error
	if q.runs, err = m.Int64Counter("artifactweave.work.runs",
		metric.WithDescription("Work queue runs, by kind and outcome"),
		metric.WithUnit("{run}")); err != nil {
		return fmt.Errorf("work: metrics: %w", err)
	}
	items, err := m.Int64ObservableGauge("artifactweave.work.items",
		metric.WithDescription("Work queue items, by kind and state"),
		metric.WithUnit("{item}"))
	if err != nil {
		return fmt.Errorf("work: metrics: %w", err)
	}
	oldest, err := m.Float64ObservableGauge("artifactweave.work.oldest_pending",
		metric.WithDescription("Age of the oldest pending work item, by kind"),
		metric.WithUnit("s"))
	if err != nil {
		return fmt.Errorf("work: metrics: %w", err)
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		stats, err := q.q.Stats(ctx)
		if err != nil {
			return fmt.Errorf("work: metrics: %w", err)
		}
		now := q.o.Now()
		for _, st := range stats {
			kind := attribute.String("kind", st.Kind)
			for state, n := range map[types.WorkState]int64{
				types.WorkPending: st.Pending, types.WorkLeased: st.Leased, types.WorkParked: st.Parked,
			} {
				o.ObserveInt64(
					items,
					n,
					metric.WithAttributes(kind, attribute.String("state", string(state))),
				)
			}
			age := 0.0
			if !st.OldestPending.IsZero() {
				age = now.Sub(st.OldestPending).Seconds()
			}
			o.ObserveFloat64(oldest, age, metric.WithAttributes(kind))
		}
		return nil
	}, items, oldest)
	if err != nil {
		return fmt.Errorf("work: metrics: %w", err)
	}
	return nil
}
