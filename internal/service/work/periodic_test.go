package work_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const periodicKind = "test.periodic"

func (w *world) enqueueKind(k, key string) {
	w.t.Helper()
	if err := w.st.Tx(context.Background(), func(ctx context.Context) error {
		return w.q.Enqueue(ctx, k, key, nil)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// recorder counts each key's runs. Its handler asks for "again" once more
// during that key's first run, and fails "bad".
type recorder struct {
	mu   sync.Mutex
	runs map[string]int
	w    *world
}

func (r *recorder) handle(ctx context.Context, it types.WorkItem) error {
	r.mu.Lock()
	r.runs[it.Key]++
	n := r.runs[it.Key]
	r.mu.Unlock()
	switch {
	case it.Key == "again" && n == 1:
		return r.w.q.Enqueue(ctx, it.Kind, it.Key, nil)
	case it.Key == "bad":
		return errors.New("bad item")
	}
	return nil
}

func (r *recorder) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[key]
}

// A periodic kind's items wait for its timer, persisted in the queue:
// WaitIdle does not wait for them, and RunDue runs every due item once per
// pass. An item asked for again during its run, and one that fails, wait
// for a later pass.
func TestRunDue(t *testing.T) {
	w := newWorld(t, work.Options{})
	r := &recorder{runs: map[string]int{}, w: w}
	if err := w.q.HandleEvery(periodicKind, time.Hour, r.handle); err != nil {
		t.Fatal(err)
	}
	if err := w.q.HandleEvery("test.zero", 0, r.handle); !errors.Is(err, work.ErrOptions) {
		t.Fatalf("an interval of zero: %v", err)
	}
	if err := w.q.HandleEvery(periodicKind, time.Hour, r.handle); !errors.Is(
		err, work.ErrDuplicateHandler,
	) {
		t.Fatalf("a second handler: %v", err)
	}
	for _, key := range []string{"a", "again", "bad"} {
		w.enqueueKind(periodicKind, key)
	}
	w.run()
	w.waitIdle()
	for _, key := range []string{"a", "again", "bad"} {
		if _, err := w.st.WorkQueue().Get(context.Background(), periodicKind, key); err != nil {
			t.Fatalf("%s ran before its timer: %v", key, err)
		}
	}

	ctx := context.Background()
	n, err := w.q.RunDue(ctx, periodicKind)
	if err != nil || n != 3 {
		t.Fatalf("first pass: %d runs, %v", n, err)
	}
	if r.count("a") != 1 || r.count("again") != 1 || r.count("bad") != 1 {
		t.Fatalf("first pass ran %v", r.runs)
	}
	if _, err := w.st.WorkQueue().Get(ctx, periodicKind, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a is done: %v", err)
	}
	// "again" is due again; "bad" waits out its backoff.
	n, err = w.q.RunDue(ctx, periodicKind)
	if err != nil || n != 1 || r.count("again") != 2 || r.count("bad") != 1 {
		t.Fatalf("second pass: %d runs, %v, %v", n, err, r.runs)
	}
	bad, err := w.st.WorkQueue().Get(ctx, periodicKind, "bad")
	if err != nil || bad.State != types.WorkPending || bad.LastError != "bad item" {
		t.Fatalf("the failed item: %+v %v", bad, err)
	}
	if _, err := w.q.RunDue(ctx, "test.unknown"); !errors.Is(err, work.ErrNoHandler) {
		t.Fatalf("an unknown kind: %v", err)
	}
}

// The timer runs a periodic kind's due items without RunDue.
func TestHandleEveryTicks(t *testing.T) {
	w := newWorld(t, work.Options{})
	ran := make(chan string, 1)
	if err := w.q.HandleEvery(periodicKind, 10*time.Millisecond,
		func(_ context.Context, it types.WorkItem) error {
			ran <- it.Key
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	w.enqueueKind(periodicKind, "t")
	w.run()
	select {
	case key := <-ran:
		if key != "t" {
			t.Fatalf("ran %q", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timer never ran the item")
	}
}

// A pass over a store that cannot lease reports it; the timer logs it and
// tries again at its next tick.
func TestRunDueStoreFailure(t *testing.T) {
	q, err := work.New(failingStore{newWorld(t, work.Options{}).st}, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.HandleEvery(periodicKind, time.Millisecond,
		func(context.Context, types.WorkItem) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RunDue(context.Background(), periodicKind); !errors.Is(err, errStore) {
		t.Fatalf("RunDue over a failing store: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := q.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
