package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// runWork covers the persistent work queue (decision 0033).
func runWork(t *testing.T, newStore func(*testing.T) Store) {
	t.Run("WorkValidation", func(t *testing.T) { testWorkValidation(t, newStore(t)) })
	t.Run("WorkLifecycle", func(t *testing.T) { testWorkLifecycle(t, newStore(t)) })
	t.Run("WorkCoalescing", func(t *testing.T) { testWorkCoalescing(t, newStore(t)) })
	t.Run("WorkFencing", func(t *testing.T) { testWorkFencing(t, newStore(t)) })
	t.Run("WorkFailures", func(t *testing.T) { testWorkFailures(t, newStore(t)) })
	t.Run("WorkLeaseKey", func(t *testing.T) { testWorkLeaseKey(t, newStore(t)) })
	t.Run("WorkTransactions", func(t *testing.T) { testWorkTransactions(t, newStore(t)) })
	t.Run("WorkConcurrency", func(t *testing.T) { testWorkConcurrency(t, newStore(t)) })
}

// lease asks for up to limit items of kinds at minute n, held until n+10.
func lease(holder string, n, limit int, kinds ...string) work.WorkLease {
	return work.WorkLease{
		Kinds:  kinds,
		Holder: holder,
		Limit:  limit,
		Until:  storekit.At(n + 10),
		Now:    storekit.At(n),
	}
}

// keys lists items as kind/key.
func keys(items []work.WorkItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Kind + "/" + it.Key
	}
	return out
}

func testWorkValidation(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	for name, c := range map[string]struct {
		kind, key string
		payload   []byte
	}{
		"no kind":      {"", "k", nil},
		"no key":       {"maven", "", nil},
		"long kind":    {strings.Repeat("k", work.MaxWorkKind+1), "k", nil},
		"long key":     {"maven", strings.Repeat("k", work.MaxWorkKey+1), nil},
		"not JSON":     {"maven", "k", []byte("{")},
		"payload size": {"maven", "k", []byte(`"` + strings.Repeat("x", work.MaxWorkPayload) + `"`)},
	} {
		if err := q.Enqueue(
			ctx,
			c.kind,
			c.key,
			c.payload,
			storekit.At(0),
		); !errors.Is(
			err,
			kernel.ErrInvalid,
		) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	for name, req := range map[string]work.WorkLease{
		"no holder":   {Limit: 1, Until: storekit.At(1), Now: storekit.At(0)},
		"no expiry":   {Holder: "w", Limit: 1, Now: storekit.At(0)},
		"past expiry": {Holder: "w", Limit: 1, Until: storekit.At(0), Now: storekit.At(0)},
	} {
		_, err := q.Lease(ctx, req)
		storekit.WantErr(t, err, kernel.ErrInvalid)
		_, _, err = q.LeaseKey(ctx, "maven", "k", req)
		if !errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("LeaseKey %s: %v", name, err)
		}
	}
	_, err := q.Get(ctx, "maven", "missing")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	if got := storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 0, 0))); len(got) != 0 {
		t.Fatalf("a zero limit leased %v", keys(got))
	}
}

func testWorkLifecycle(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	storekit.Must(
		t,
		q.Enqueue(
			ctx,
			"maven",
			"org.acme:lib",
			[]byte(`{"folder":"org/acme/lib"}`),
			storekit.At(0),
		),
	)
	storekit.Must(t, q.Enqueue(ctx, "maven", "org.acme:app", nil, storekit.At(1)))
	storekit.Must(t, q.Enqueue(ctx, "npm", "left-pad", nil, storekit.At(2)))
	lib := work.WorkItem{
		Kind:      "maven",
		Key:       "org.acme:lib",
		Payload:   []byte(`{"folder":"org/acme/lib"}`),
		State:     work.WorkPending,
		NotBefore: storekit.At(0),
		Enqueued:  storekit.At(0),
		Updated:   storekit.At(0),
	}
	storekit.Same(
		t,
		"Get",
		lib,
		storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "org.acme:lib")),
	)
	storekit.Same(t, "Stats", []work.WorkStats{
		{Kind: "maven", Pending: 2, OldestPending: storekit.At(0)},
		{Kind: "npm", Pending: 1, OldestPending: storekit.At(2)},
	}, storekit.Get[[]work.WorkStats](t)(q.Stats(ctx)))

	// The longest due goes first, and Kinds limits what is taken.
	got := storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w1", 3, 1, "maven")))
	taken := lib
	taken.State, taken.LeasedBy, taken.LeaseUntil = work.WorkLeased, "w1", storekit.At(13)
	taken.Generation, taken.Attempts, taken.Updated = 1, 1, storekit.At(3)
	storekit.Same(t, "Lease", []work.WorkItem{taken}, got)
	got = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w2", 3, 10)))
	if diff := keys(got); strings.Join(diff, " ") != "maven/org.acme:app npm/left-pad" {
		t.Fatalf("second lease took %v", diff)
	}
	if again := storekit.Get[[]work.WorkItem](
		t,
	)(
		q.Lease(ctx, lease("w3", 4, 10)),
	); len(
		again,
	) != 0 {
		t.Fatalf("leased items were taken again: %v", keys(again))
	}
	storekit.Same(t, "Stats while leased", []work.WorkStats{
		{Kind: "maven", Leased: 2}, {Kind: "npm", Leased: 1},
	}, storekit.Get[[]work.WorkStats](t)(q.Stats(ctx)))

	// Extend keeps the lease past its first expiry.
	storekit.Must(t, q.Extend(ctx, "maven", "org.acme:lib", 1, storekit.At(30), storekit.At(5)))
	if again := storekit.Get[[]work.WorkItem](
		t,
	)(
		q.Lease(ctx, lease("w3", 20, 10, "maven")),
	); strings.Join(
		keys(again),
		" ",
	) != "maven/org.acme:app" {
		t.Fatalf("after Extend, lease at 20 took %v, want only the unextended item", keys(again))
	}

	// Complete deletes an item no work was asked for during its run.
	storekit.Must(t, q.Complete(ctx, "npm", "left-pad", 1, storekit.At(6)))
	_, err := q.Get(ctx, "npm", "left-pad")
	storekit.WantErr(t, err, kernel.ErrNotFound)

	// Release hands the run back without counting it.
	storekit.Must(t, q.Release(ctx, "maven", "org.acme:lib", 1, storekit.At(7)))
	released := storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "org.acme:lib"))
	if released.State != work.WorkPending || released.Attempts != 0 || released.LeasedBy != "" ||
		!released.LeaseUntil.IsZero() || !released.NotBefore.Equal(storekit.Want(storekit.At(7))) {
		t.Fatalf("after Release: %+v", released)
	}
}

func testWorkCoalescing(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`1`), storekit.At(0)))
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`2`), storekit.At(1)))
	it := storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if string(it.Payload) != "2" || !it.Enqueued.Equal(storekit.Want(storekit.At(0))) ||
		!it.NotBefore.Equal(storekit.Want(storekit.At(0))) {
		t.Fatalf("a second enqueue of a pending key: %+v", it)
	}

	// Work asked for during a run marks it to run once more.
	_ = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 2, 1)))
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`3`), storekit.At(3)))
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`4`), storekit.At(4)))
	it = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != work.WorkLeased || !it.DirtySince.Equal(storekit.Want(storekit.At(3))) ||
		string(it.Payload) != "4" {
		t.Fatalf("enqueues during a run: %+v", it)
	}
	storekit.Must(t, q.Complete(ctx, "maven", "g:a", 1, storekit.At(5)))
	it = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	wantItem := work.WorkItem{
		Kind: "maven", Key: "g:a", Payload: []byte(`4`), State: work.WorkPending,
		Generation: 1, NotBefore: storekit.At(5), Enqueued: storekit.At(3), Updated: storekit.At(5),
	}
	storekit.Same(t, "after a run with work asked for during it", wantItem, it)

	// A new lease covers work asked for before it began.
	_ = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 6, 1)))
	it = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if !it.DirtySince.IsZero() || it.Generation != 2 || it.Attempts != 1 {
		t.Fatalf("second lease: %+v", it)
	}
	storekit.Must(t, q.Complete(ctx, "maven", "g:a", 2, storekit.At(7)))
	_, err := q.Get(ctx, "maven", "g:a")
	storekit.WantErr(t, err, kernel.ErrNotFound)
}

func testWorkFencing(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", nil, storekit.At(0)))
	_ = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, work.WorkLease{
		Holder: "w1", Limit: 1, Until: storekit.At(2), Now: storekit.At(1),
	}))
	// The lease ran out; another worker takes the item over.
	got := storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w2", 3, 1)))
	if len(got) != 1 || got[0].Generation != 2 || got[0].Attempts != 2 || got[0].LeasedBy != "w2" {
		t.Fatalf("taking over an expired lease: %+v", got)
	}
	for name, err := range map[string]error{
		"Complete": q.Complete(ctx, "maven", "g:a", 1, storekit.At(4)),
		"Fail":     q.Fail(ctx, "maven", "g:a", 1, work.WorkFailure{Error: "x", RetryAt: storekit.At(5), Now: storekit.At(4)}),
		"Park":     q.Fail(ctx, "maven", "g:a", 1, work.WorkFailure{Error: "x", Park: true, Now: storekit.At(4)}),
		"Extend":   q.Extend(ctx, "maven", "g:a", 1, storekit.At(20), storekit.At(4)),
		"Release":  q.Release(ctx, "maven", "g:a", 1, storekit.At(4)),
		"missing":  q.Complete(ctx, "maven", "missing", 1, storekit.At(4)),
	} {
		if !errors.Is(err, kernel.ErrConflict) {
			t.Errorf("%s by the lapsed run: %v, want ErrConflict", name, err)
		}
	}
	storekit.Must(t, q.Complete(ctx, "maven", "g:a", 2, storekit.At(5)))
	storekit.WantErr(t, q.Complete(ctx, "maven", "g:a", 2, storekit.At(5)), kernel.ErrConflict)
}

func testWorkFailures(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", nil, storekit.At(0)))
	_ = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 1, 1)))
	storekit.Must(
		t,
		q.Fail(
			ctx,
			"maven",
			"g:a",
			1,
			work.WorkFailure{Error: "boom", RetryAt: storekit.At(30), Now: storekit.At(2)},
		),
	)
	it := storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != work.WorkPending || it.LastError != "boom" || it.Attempts != 1 ||
		!it.NotBefore.Equal(storekit.Want(storekit.At(30))) || it.LeasedBy != "" {
		t.Fatalf("after Fail: %+v", it)
	}
	if got := storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 29, 1))); len(got) != 0 {
		t.Fatal("an item was leased before its retry time")
	}
	got := storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 30, 1)))
	if len(got) != 1 || got[0].Attempts != 2 {
		t.Fatalf("retry: %+v", got)
	}
	storekit.Must(
		t,
		q.Fail(
			ctx,
			"maven",
			"g:a",
			2,
			work.WorkFailure{Error: "again", Park: true, Now: storekit.At(31)},
		),
	)
	it = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != work.WorkParked || it.LastError != "again" {
		t.Fatalf("after parking: %+v", it)
	}
	if got := storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 100, 1))); len(got) != 0 {
		t.Fatal("a parked item was leased")
	}
	storekit.Same(t, "Stats with a parked item", []work.WorkStats{{Kind: "maven", Parked: 1}},
		storekit.Get[[]work.WorkStats](t)(q.Stats(ctx)))

	// A fresh enqueue gives a parked key another chance.
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", nil, storekit.At(40)))
	it = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != work.WorkPending || it.Attempts != 0 ||
		!it.NotBefore.Equal(storekit.Want(storekit.At(40))) ||
		!it.Enqueued.Equal(storekit.Want(storekit.At(40))) {
		t.Fatalf("enqueue of a parked key: %+v", it)
	}

	// Work asked for during a run that fails is retried, not parked.
	_ = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 41, 1)))
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", nil, storekit.At(42)))
	storekit.Must(t, q.Fail(ctx, "maven", "g:a", 3, work.WorkFailure{
		Error: "x", Park: true, RetryAt: storekit.At(50), Now: storekit.At(43),
	}))
	it = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != work.WorkPending || !it.NotBefore.Equal(storekit.Want(storekit.At(50))) ||
		!it.DirtySince.IsZero() {
		t.Fatalf("a failed run with work asked for during it: %+v", it)
	}
}

func testWorkLeaseKey(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	_, _, err := q.LeaseKey(ctx, "maven", "missing", lease("r", 0, 0))
	storekit.WantErr(t, err, kernel.ErrNotFound)

	// A pending item is taken whatever its schedule.
	storekit.Must(t, q.Enqueue(ctx, "maven", "g:a", nil, storekit.At(0)))
	_ = storekit.Get[[]work.WorkItem](t)(q.Lease(ctx, lease("w", 1, 1)))
	storekit.Must(
		t,
		q.Fail(
			ctx,
			"maven",
			"g:a",
			1,
			work.WorkFailure{RetryAt: storekit.At(60), Now: storekit.At(2)},
		),
	)
	it, took, err := q.LeaseKey(ctx, "maven", "g:a", lease("reader", 3, 0))
	storekit.Must(t, err)
	if !took || it.LeasedBy != "reader" || it.Generation != 2 {
		t.Fatalf("LeaseKey of a pending item: took=%v %+v", took, it)
	}
	// A held lease is not taken; the item is returned as it stands.
	it, took, err = q.LeaseKey(ctx, "maven", "g:a", lease("other", 4, 0))
	storekit.Must(t, err)
	if took || it.LeasedBy != "reader" {
		t.Fatalf("LeaseKey of a held item: took=%v %+v", took, it)
	}
	// An expired one is.
	it, took, err = q.LeaseKey(ctx, "maven", "g:a", lease("other", 14, 0))
	storekit.Must(t, err)
	if !took || it.LeasedBy != "other" || it.Generation != 3 {
		t.Fatalf("LeaseKey of an expired item: took=%v %+v", took, it)
	}
	// A parked one is not.
	storekit.Must(
		t,
		q.Fail(ctx, "maven", "g:a", 3, work.WorkFailure{Park: true, Now: storekit.At(15)}),
	)
	it, took, err = q.LeaseKey(ctx, "maven", "g:a", lease("r", 16, 0))
	storekit.Must(t, err)
	if took || it.State != work.WorkParked {
		t.Fatalf("LeaseKey of a parked item: took=%v %+v", took, it)
	}
}

func testWorkTransactions(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	err := s.Tx(ctx, func(ctx context.Context) error {
		storekit.Must(t, q.Enqueue(ctx, "maven", "rolled-back", nil, storekit.At(0)))
		return errBoom
	})
	storekit.WantErr(t, err, errBoom)
	_, err = q.Get(ctx, "maven", "rolled-back")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		return q.Enqueue(ctx, "maven", "committed", nil, storekit.At(0))
	}))
	_ = storekit.Get[work.WorkItem](t)(q.Get(ctx, "maven", "committed"))
}

func testWorkConcurrency(t *testing.T, s Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	const items, workers = 40, 8

	// Concurrent enqueues of one key leave one item.
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			if err := q.Enqueue(
				ctx,
				"race",
				"one",
				[]byte(fmt.Sprint(i)),
				storekit.At(0),
			); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if st := storekit.Get[[]work.WorkStats](t)(q.Stats(ctx)); len(st) != 1 || st[0].Pending != 1 {
		t.Fatalf("concurrent enqueues of one key: %+v", st)
	}

	// Concurrent workers never take the same item.
	for i := range items {
		storekit.Must(t, q.Enqueue(ctx, "bulk", fmt.Sprintf("k%02d", i), nil, storekit.At(0)))
	}
	var (
		mu    sync.Mutex
		taken = map[string]string{}
	)
	for w := range workers {
		holder := fmt.Sprintf("w%d", w)
		wg.Go(func() {
			for {
				got, err := q.Lease(ctx, lease(holder, 1, 3, "bulk"))
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) == 0 {
					return
				}
				for _, it := range got {
					mu.Lock()
					if prev, dup := taken[it.Key]; dup {
						t.Errorf("%s taken by %s and %s", it.Key, prev, holder)
					}
					taken[it.Key] = holder
					mu.Unlock()
					if err := q.Complete(
						ctx,
						it.Kind,
						it.Key,
						it.Generation,
						storekit.At(2),
					); err != nil {
						t.Error(err)
					}
				}
			}
		})
	}
	wg.Wait()
	if len(taken) != items {
		t.Fatalf("%d of %d items were run", len(taken), items)
	}
	if st := storekit.Get[[]work.WorkStats](
		t,
	)(
		q.Stats(ctx),
	); len(st) != 1 ||
		st[0].Kind != "race" {
		t.Fatalf("after the bulk run: %+v", st)
	}
}
