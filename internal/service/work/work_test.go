package work_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const kind = "test.derive"

// world is a queue over an in-memory store with a metric reader.
type world struct {
	t      *testing.T
	st     store.Store
	q      *work.Queue
	reader *sdkmetric.ManualReader
}

func newWorld(t *testing.T, o work.Options) *world {
	t.Helper()
	st := inmem.New()
	reader := sdkmetric.NewManualReader()
	o.Meters = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if o.Poll == 0 {
		o.Poll = 5 * time.Millisecond
	}
	q, err := work.New(st, o)
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, st: st, q: q, reader: reader}
}

// run starts the queue and stops it when the test ends.
func (w *world) run() {
	w.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = w.q.Run(ctx)
		close(done)
	}()
	w.t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (w *world) enqueue(key string) {
	w.t.Helper()
	if err := w.st.Tx(context.Background(), func(ctx context.Context) error {
		return w.q.Enqueue(ctx, kind, key, []byte(`{"key":"`+key+`"}`))
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) item(key string) (types.WorkItem, error) {
	return w.st.WorkQueue().Get(context.Background(), kind, key)
}

func (w *world) waitIdle() {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.q.WaitIdle(ctx); err != nil {
		w.t.Fatal(err)
	}
}

// metrics returns the runs counter by outcome, and the items gauge by state,
// for kind.
func (w *world) metrics() (runs, items map[string]int64, oldest float64) {
	w.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := w.reader.Collect(context.Background(), &rm); err != nil {
		w.t.Fatal(err)
	}
	runs, items = map[string]int64{}, map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					o, _ := p.Attributes.Value("outcome")
					runs[o.AsString()] += p.Value
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					s, _ := p.Attributes.Value("state")
					items[s.AsString()] += p.Value
				}
			case metricdata.Gauge[float64]:
				for _, p := range d.DataPoints {
					oldest = p.Value
				}
			}
		}
	}
	return runs, items, oldest
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBackoff(t *testing.T) {
	for attempt, want := range map[int]time.Duration{
		0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second,
		10: 512 * time.Second, 11: 10 * time.Minute, 100: 10 * time.Minute,
	} {
		if got := work.Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %s, want %s", attempt, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	for _, o := range []work.Options{{Workers: -1}, {Lease: -1}, {MaxAttempts: -1}, {Poll: -1}} {
		if _, err := work.New(inmem.New(), o); !errors.Is(err, work.ErrOptions) {
			t.Errorf("New(%+v): %v, want ErrOptions", o, err)
		}
	}
	q, err := work.New(inmem.New(), work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Handle(
		kind,
		func(context.Context, types.WorkItem) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	if err := q.Handle(
		kind,
		func(context.Context, types.WorkItem) error { return nil },
	); !errors.Is(err, work.ErrDuplicateHandler) {
		t.Fatalf("a second handler for one kind: %v", err)
	}
}

// Enqueued work runs once its transaction commits, and work asked for again
// for a key while it waits runs once.
func TestRunCompletes(t *testing.T) {
	w := newWorld(t, work.Options{Workers: 3})
	var (
		mu   sync.Mutex
		seen []string
	)
	if err := w.q.Handle(kind, func(_ context.Context, it types.WorkItem) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, it.Key+" "+string(it.Payload))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.enqueue("a")
	w.enqueue("a")
	w.enqueue("b")
	_, items, oldest := w.metrics()
	if items["pending"] != 2 || oldest < 0 {
		t.Fatalf("before running: items %v, oldest %v", items, oldest)
	}
	w.run()
	w.waitIdle()
	mu.Lock()
	got := strings.Join(seen, ", ")
	mu.Unlock()
	if len(seen) != 2 || !strings.Contains(got, `a {"key":"a"}`) ||
		!strings.Contains(got, `b {"key":"b"}`) {
		t.Fatalf("handled %q", got)
	}
	for _, k := range []string{"a", "b"} {
		if _, err := w.item(k); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s left in the queue: %v", k, err)
		}
	}
	if runs, _, _ := w.metrics(); runs["completed"] != 2 {
		t.Fatalf("runs %v", runs)
	}

	// A rolled-back enqueue asks for nothing.
	boom := errors.New("boom")
	err := w.st.Tx(context.Background(), func(ctx context.Context) error {
		if err := w.q.Enqueue(ctx, kind, "rolled-back", nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := w.item("rolled-back"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a rolled-back enqueue left %v", err)
	}
	if err := w.q.Enqueue(context.Background(), kind, "", nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("an empty key: %v", err)
	}
}

// A failing item is retried after its backoff and parked once it has used
// its attempts; a panic counts as a failure.
func TestRetryAndPark(t *testing.T) {
	w := newWorld(t, work.Options{
		Workers: 1, MaxAttempts: 3,
		Backoff: func(int) time.Duration { return time.Millisecond },
	})
	var calls atomic.Int64
	if err := w.q.Handle(kind, func(context.Context, types.WorkItem) error {
		if calls.Add(1) == 2 {
			panic("kaboom")
		}
		return errors.New("still broken")
	}); err != nil {
		t.Fatal(err)
	}
	w.enqueue("k")
	w.run()
	eventually(t, "the item to park", func() bool {
		it, err := w.item("k")
		return err == nil && it.State == types.WorkParked
	})
	it, _ := w.item("k")
	if it.Attempts != 3 || it.LastError != "still broken" || calls.Load() != 3 {
		t.Fatalf("parked item %+v after %d calls", it, calls.Load())
	}
	w.waitIdle()
	runs, items, _ := w.metrics()
	if runs["failed"] != 2 || runs["parked"] != 1 || items["parked"] != 1 {
		t.Fatalf("runs %v, items %v", runs, items)
	}
}

func TestPanicIsAFailure(t *testing.T) {
	w := newWorld(
		t,
		work.Options{Workers: 1, Backoff: func(int) time.Duration { return time.Hour }},
	)
	if err := w.q.Handle(
		kind,
		func(context.Context, types.WorkItem) error { panic("kaboom") },
	); err != nil {
		t.Fatal(err)
	}
	w.enqueue("k")
	w.run()
	eventually(t, "the panic to be recorded", func() bool {
		it, err := w.item("k")
		return err == nil && it.State == types.WorkPending &&
			strings.Contains(it.LastError, "kaboom") &&
			strings.Contains(it.LastError, work.ErrPanic.Error())
	})
}

// A run cut short by the queue stopping is handed back, not counted.
func TestStopReleases(t *testing.T) {
	w := newWorld(t, work.Options{Workers: 1})
	started := make(chan struct{})
	if err := w.q.Handle(kind, func(ctx context.Context, _ types.WorkItem) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	w.enqueue("k")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = w.q.Run(ctx)
		close(done)
	}()
	<-started
	cancel()
	<-done
	it, err := w.item("k")
	if err != nil || it.State != types.WorkPending || it.Attempts != 0 || it.LeasedBy != "" {
		t.Fatalf("after stopping: %+v %v", it, err)
	}
	if runs, _, _ := w.metrics(); runs["released"] != 1 {
		t.Fatalf("runs %v", runs)
	}
}

// A long run keeps its lease, and a run whose lease is lost is cancelled and
// recorded as lost.
func TestHeartbeat(t *testing.T) {
	w := newWorld(t, work.Options{Workers: 1, Lease: 40 * time.Millisecond})
	release := make(chan struct{})
	var lost atomic.Bool
	if err := w.q.Handle(kind, func(ctx context.Context, it types.WorkItem) error {
		if it.Key == "slow" {
			time.Sleep(150 * time.Millisecond)
			return nil
		}
		select {
		case <-ctx.Done():
			lost.Store(true)
			return ctx.Err()
		case <-release:
			return nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	w.enqueue("slow")
	w.run()
	eventually(t, "the slow run to complete", func() bool {
		_, err := w.item("slow")
		return errors.Is(err, store.ErrNotFound)
	})
	if it, _, err := w.st.WorkQueue().LeaseKey(context.Background(), kind, "slow", store.WorkLease{
		Holder: "x", Until: time.Now().Add(time.Hour), Now: time.Now(),
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the slow item was run twice: %+v %v", it, err)
	}

	w.enqueue("stolen")
	eventually(t, "the run to start", func() bool {
		it, err := w.item("stolen")
		return err == nil && it.State == types.WorkLeased
	})
	it, _ := w.item("stolen")
	// Another node completes the item, as one would after taking it over.
	if err := w.st.WorkQueue().Complete(context.Background(), kind, "stolen", it.Generation,
		time.Now()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run to see its lease lost", lost.Load)
	close(release)
	eventually(t, "the lost run to be recorded", func() bool {
		runs, _, _ := w.metrics()
		return runs["lost"] == 1
	})
}

func TestEnsureFresh(t *testing.T) {
	w := newWorld(t, work.Options{Backoff: func(int) time.Duration { return time.Hour }})
	var (
		calls atomic.Int64
		fail  atomic.Bool
	)
	if err := w.q.Handle(kind, func(context.Context, types.WorkItem) error {
		calls.Add(1)
		if fail.Load() {
			return errors.New("broken")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := w.q.EnsureFresh(ctx, "other.kind", "k"); !errors.Is(err, work.ErrNoHandler) {
		t.Fatalf("an unregistered kind: %v", err)
	}
	if err := w.q.EnsureFresh(ctx, kind, "none"); err != nil || calls.Load() != 0 {
		t.Fatalf("nothing owed: %v, %d calls", err, calls.Load())
	}
	// Owed work runs in the caller's context, without a running queue.
	w.enqueue("k")
	if err := w.q.EnsureFresh(ctx, kind, "k"); err != nil || calls.Load() != 1 {
		t.Fatalf("owed work: %v, %d calls", err, calls.Load())
	}
	if _, err := w.item("k"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after EnsureFresh: %v", err)
	}
	// A failing run returns its error and leaves the item to retry.
	fail.Store(true)
	w.enqueue("k")
	if err := w.q.EnsureFresh(
		ctx,
		kind,
		"k",
	); err == nil ||
		!strings.Contains(err.Error(), "broken") {
		t.Fatalf("a failing run: %v", err)
	}
	// A run held elsewhere is waited for.
	fail.Store(false)
	held, took, err := w.st.WorkQueue().LeaseKey(ctx, kind, "k", store.WorkLease{
		Holder: "elsewhere", Until: time.Now().Add(time.Hour), Now: time.Now(),
	})
	if err != nil || !took {
		t.Fatalf("leasing elsewhere: %v %v", took, err)
	}
	done := make(chan error, 1)
	go func() { done <- w.q.EnsureFresh(ctx, kind, "k") }()
	time.Sleep(20 * time.Millisecond)
	if err := w.st.WorkQueue().Complete(ctx, kind, "k", held.Generation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("waiting on a run elsewhere: %v", err)
	}
	// A parked item is reported, and a cancelled wait ends.
	w.enqueue("p")
	parked, _, _ := w.st.WorkQueue().LeaseKey(ctx, kind, "p", store.WorkLease{
		Holder: "elsewhere", Until: time.Now().Add(time.Hour), Now: time.Now(),
	})
	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := w.q.EnsureFresh(cctx, kind, "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting past the deadline: %v", err)
	}
	if err := w.st.WorkQueue().Fail(ctx, kind, "p", parked.Generation, store.WorkFailure{
		Error: "gave up", Park: true, Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.q.EnsureFresh(ctx, kind, "p"); !errors.Is(err, work.ErrParked) ||
		!strings.Contains(err.Error(), "gave up") {
		t.Fatalf("a parked item: %v", err)
	}
}

func TestWaitIdleDeadline(t *testing.T) {
	w := newWorld(t, work.Options{})
	if err := w.q.Handle(
		kind,
		func(context.Context, types.WorkItem) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	w.enqueue("k")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := w.q.WaitIdle(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitIdle with work owed and no workers: %v", err)
	}
}

// With no handlers there is nothing to lease; Run waits for its context.
func TestRunWithoutHandlers(t *testing.T) {
	q, err := work.New(inmem.New(), work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

// failingQueue fails every lease and every Stats call.
type failingQueue struct{ store.WorkQueue }

var errStore = errors.New("store down")

func (failingQueue) Lease(context.Context, store.WorkLease) ([]types.WorkItem, error) {
	return nil, errStore
}

func (failingQueue) Stats(context.Context) ([]types.WorkStats, error) { return nil, errStore }

type failingStore struct{ store.Store }

func (f failingStore) WorkQueue() store.WorkQueue { return failingQueue{f.Store.WorkQueue()} }

func TestStoreFailures(t *testing.T) {
	st := failingStore{inmem.New()}
	reader := sdkmetric.NewManualReader()
	q, err := work.New(st, work.Options{
		Poll: time.Millisecond, Meters: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Handle(
		kind,
		func(context.Context, types.WorkItem) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := q.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.WaitIdle(context.Background()); !errors.Is(err, errStore) {
		t.Fatalf("WaitIdle over a failing store: %v", err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); !errors.Is(err, errStore) {
		t.Fatalf("collecting over a failing store: %v", err)
	}
}

func TestPermanentRefusalParksWithoutRetry(t *testing.T) {
	w := newWorld(t, work.Options{})
	if err := w.q.Handle(kind, func(context.Context, types.WorkItem) error {
		return errors.Join(work.ErrPermanent, errors.New("authority revoked"))
	}); err != nil {
		t.Fatal(err)
	}
	w.enqueue("revoked")
	if _, err := w.q.RunDue(t.Context(), kind); err != nil {
		t.Fatal(err)
	}
	record, err := w.item("revoked")
	if err != nil || record.State != types.WorkParked || record.Attempts != 1 ||
		!strings.Contains(record.LastError, "authority revoked") {
		t.Fatal(record, err)
	}
	if n, err := w.q.RunDue(t.Context(), kind); err != nil || n != 0 {
		t.Fatal("permanent refusal retried", n, err)
	}
}
