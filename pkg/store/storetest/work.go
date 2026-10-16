package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// runWork covers the persistent work queue (decision 0033).
func runWork(t *testing.T, newStore Factory) {
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
func lease(holder string, n, limit int, kinds ...string) store.WorkLease {
	return store.WorkLease{
		Kinds:  kinds,
		Holder: holder,
		Limit:  limit,
		Until:  at(n + 10),
		Now:    at(n),
	}
}

// keys lists items as kind/key.
func keys(items []types.WorkItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Kind + "/" + it.Key
	}
	return out
}

func testWorkValidation(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	for name, c := range map[string]struct {
		kind, key string
		payload   []byte
	}{
		"no kind":      {"", "k", nil},
		"no key":       {"maven", "", nil},
		"long kind":    {strings.Repeat("k", store.MaxWorkKind+1), "k", nil},
		"long key":     {"maven", strings.Repeat("k", store.MaxWorkKey+1), nil},
		"not JSON":     {"maven", "k", []byte("{")},
		"payload size": {"maven", "k", []byte(`"` + strings.Repeat("x", store.MaxWorkPayload) + `"`)},
	} {
		if err := q.Enqueue(
			ctx,
			c.kind,
			c.key,
			c.payload,
			at(0),
		); !errors.Is(
			err,
			store.ErrInvalid,
		) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	for name, req := range map[string]store.WorkLease{
		"no holder":   {Limit: 1, Until: at(1), Now: at(0)},
		"no expiry":   {Holder: "w", Limit: 1, Now: at(0)},
		"past expiry": {Holder: "w", Limit: 1, Until: at(0), Now: at(0)},
	} {
		_, err := q.Lease(ctx, req)
		wantErr(t, err, store.ErrInvalid)
		_, _, err = q.LeaseKey(ctx, "maven", "k", req)
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("LeaseKey %s: %v", name, err)
		}
	}
	_, err := q.Get(ctx, "maven", "missing")
	wantErr(t, err, store.ErrNotFound)
	if got := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 0, 0))); len(got) != 0 {
		t.Fatalf("a zero limit leased %v", keys(got))
	}
}

func testWorkLifecycle(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	must(t, q.Enqueue(ctx, "maven", "org.acme:lib", []byte(`{"folder":"org/acme/lib"}`), at(0)))
	must(t, q.Enqueue(ctx, "maven", "org.acme:app", nil, at(1)))
	must(t, q.Enqueue(ctx, "npm", "left-pad", nil, at(2)))
	lib := types.WorkItem{
		Kind: "maven", Key: "org.acme:lib", Payload: []byte(`{"folder":"org/acme/lib"}`),
		State: types.WorkPending, NotBefore: at(0), Enqueued: at(0), Updated: at(0),
	}
	same(t, "Get", lib, get[types.WorkItem](t)(q.Get(ctx, "maven", "org.acme:lib")))
	same(t, "Stats", []types.WorkStats{
		{Kind: "maven", Pending: 2, OldestPending: at(0)},
		{Kind: "npm", Pending: 1, OldestPending: at(2)},
	}, get[[]types.WorkStats](t)(q.Stats(ctx)))

	// The longest due goes first, and Kinds limits what is taken.
	got := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w1", 3, 1, "maven")))
	taken := lib
	taken.State, taken.LeasedBy, taken.LeaseUntil = types.WorkLeased, "w1", at(13)
	taken.Generation, taken.Attempts, taken.Updated = 1, 1, at(3)
	same(t, "Lease", []types.WorkItem{taken}, got)
	got = get[[]types.WorkItem](t)(q.Lease(ctx, lease("w2", 3, 10)))
	if diff := keys(got); strings.Join(diff, " ") != "maven/org.acme:app npm/left-pad" {
		t.Fatalf("second lease took %v", diff)
	}
	if again := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w3", 4, 10))); len(again) != 0 {
		t.Fatalf("leased items were taken again: %v", keys(again))
	}
	same(t, "Stats while leased", []types.WorkStats{
		{Kind: "maven", Leased: 2}, {Kind: "npm", Leased: 1},
	}, get[[]types.WorkStats](t)(q.Stats(ctx)))

	// Extend keeps the lease past its first expiry.
	must(t, q.Extend(ctx, "maven", "org.acme:lib", 1, at(30), at(5)))
	if again := get[[]types.WorkItem](
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
	must(t, q.Complete(ctx, "npm", "left-pad", 1, at(6)))
	_, err := q.Get(ctx, "npm", "left-pad")
	wantErr(t, err, store.ErrNotFound)

	// Release hands the run back without counting it.
	must(t, q.Release(ctx, "maven", "org.acme:lib", 1, at(7)))
	released := get[types.WorkItem](t)(q.Get(ctx, "maven", "org.acme:lib"))
	if released.State != types.WorkPending || released.Attempts != 0 || released.LeasedBy != "" ||
		!released.LeaseUntil.IsZero() || !released.NotBefore.Equal(want(at(7))) {
		t.Fatalf("after Release: %+v", released)
	}
}

func testWorkCoalescing(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`1`), at(0)))
	must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`2`), at(1)))
	it := get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if string(it.Payload) != "2" || !it.Enqueued.Equal(want(at(0))) ||
		!it.NotBefore.Equal(want(at(0))) {
		t.Fatalf("a second enqueue of a pending key: %+v", it)
	}

	// Work asked for during a run marks it to run once more.
	_ = get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 2, 1)))
	must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`3`), at(3)))
	must(t, q.Enqueue(ctx, "maven", "g:a", []byte(`4`), at(4)))
	it = get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != types.WorkLeased || !it.DirtySince.Equal(want(at(3))) ||
		string(it.Payload) != "4" {
		t.Fatalf("enqueues during a run: %+v", it)
	}
	must(t, q.Complete(ctx, "maven", "g:a", 1, at(5)))
	it = get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	wantItem := types.WorkItem{
		Kind: "maven", Key: "g:a", Payload: []byte(`4`), State: types.WorkPending,
		Generation: 1, NotBefore: at(5), Enqueued: at(3), Updated: at(5),
	}
	same(t, "after a run with work asked for during it", wantItem, it)

	// A new lease covers work asked for before it began.
	_ = get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 6, 1)))
	it = get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if !it.DirtySince.IsZero() || it.Generation != 2 || it.Attempts != 1 {
		t.Fatalf("second lease: %+v", it)
	}
	must(t, q.Complete(ctx, "maven", "g:a", 2, at(7)))
	_, err := q.Get(ctx, "maven", "g:a")
	wantErr(t, err, store.ErrNotFound)
}

func testWorkFencing(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	must(t, q.Enqueue(ctx, "maven", "g:a", nil, at(0)))
	_ = get[[]types.WorkItem](t)(q.Lease(ctx, store.WorkLease{
		Holder: "w1", Limit: 1, Until: at(2), Now: at(1),
	}))
	// The lease ran out; another worker takes the item over.
	got := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w2", 3, 1)))
	if len(got) != 1 || got[0].Generation != 2 || got[0].Attempts != 2 || got[0].LeasedBy != "w2" {
		t.Fatalf("taking over an expired lease: %+v", got)
	}
	for name, err := range map[string]error{
		"Complete": q.Complete(ctx, "maven", "g:a", 1, at(4)),
		"Fail":     q.Fail(ctx, "maven", "g:a", 1, store.WorkFailure{Error: "x", RetryAt: at(5), Now: at(4)}),
		"Park":     q.Fail(ctx, "maven", "g:a", 1, store.WorkFailure{Error: "x", Park: true, Now: at(4)}),
		"Extend":   q.Extend(ctx, "maven", "g:a", 1, at(20), at(4)),
		"Release":  q.Release(ctx, "maven", "g:a", 1, at(4)),
		"missing":  q.Complete(ctx, "maven", "missing", 1, at(4)),
	} {
		if !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s by the lapsed run: %v, want ErrConflict", name, err)
		}
	}
	must(t, q.Complete(ctx, "maven", "g:a", 2, at(5)))
	wantErr(t, q.Complete(ctx, "maven", "g:a", 2, at(5)), store.ErrConflict)
}

func testWorkFailures(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	must(t, q.Enqueue(ctx, "maven", "g:a", nil, at(0)))
	_ = get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 1, 1)))
	must(
		t,
		q.Fail(
			ctx,
			"maven",
			"g:a",
			1,
			store.WorkFailure{Error: "boom", RetryAt: at(30), Now: at(2)},
		),
	)
	it := get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != types.WorkPending || it.LastError != "boom" || it.Attempts != 1 ||
		!it.NotBefore.Equal(want(at(30))) || it.LeasedBy != "" {
		t.Fatalf("after Fail: %+v", it)
	}
	if got := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 29, 1))); len(got) != 0 {
		t.Fatal("an item was leased before its retry time")
	}
	got := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 30, 1)))
	if len(got) != 1 || got[0].Attempts != 2 {
		t.Fatalf("retry: %+v", got)
	}
	must(
		t,
		q.Fail(ctx, "maven", "g:a", 2, store.WorkFailure{Error: "again", Park: true, Now: at(31)}),
	)
	it = get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != types.WorkParked || it.LastError != "again" {
		t.Fatalf("after parking: %+v", it)
	}
	if got := get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 100, 1))); len(got) != 0 {
		t.Fatal("a parked item was leased")
	}
	same(t, "Stats with a parked item", []types.WorkStats{{Kind: "maven", Parked: 1}},
		get[[]types.WorkStats](t)(q.Stats(ctx)))

	// A fresh enqueue gives a parked key another chance.
	must(t, q.Enqueue(ctx, "maven", "g:a", nil, at(40)))
	it = get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != types.WorkPending || it.Attempts != 0 || !it.NotBefore.Equal(want(at(40))) ||
		!it.Enqueued.Equal(want(at(40))) {
		t.Fatalf("enqueue of a parked key: %+v", it)
	}

	// Work asked for during a run that fails is retried, not parked.
	_ = get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 41, 1)))
	must(t, q.Enqueue(ctx, "maven", "g:a", nil, at(42)))
	must(t, q.Fail(ctx, "maven", "g:a", 3, store.WorkFailure{
		Error: "x", Park: true, RetryAt: at(50), Now: at(43),
	}))
	it = get[types.WorkItem](t)(q.Get(ctx, "maven", "g:a"))
	if it.State != types.WorkPending || !it.NotBefore.Equal(want(at(50))) ||
		!it.DirtySince.IsZero() {
		t.Fatalf("a failed run with work asked for during it: %+v", it)
	}
}

func testWorkLeaseKey(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	_, _, err := q.LeaseKey(ctx, "maven", "missing", lease("r", 0, 0))
	wantErr(t, err, store.ErrNotFound)

	// A pending item is taken whatever its schedule.
	must(t, q.Enqueue(ctx, "maven", "g:a", nil, at(0)))
	_ = get[[]types.WorkItem](t)(q.Lease(ctx, lease("w", 1, 1)))
	must(t, q.Fail(ctx, "maven", "g:a", 1, store.WorkFailure{RetryAt: at(60), Now: at(2)}))
	it, took, err := q.LeaseKey(ctx, "maven", "g:a", lease("reader", 3, 0))
	must(t, err)
	if !took || it.LeasedBy != "reader" || it.Generation != 2 {
		t.Fatalf("LeaseKey of a pending item: took=%v %+v", took, it)
	}
	// A held lease is not taken; the item is returned as it stands.
	it, took, err = q.LeaseKey(ctx, "maven", "g:a", lease("other", 4, 0))
	must(t, err)
	if took || it.LeasedBy != "reader" {
		t.Fatalf("LeaseKey of a held item: took=%v %+v", took, it)
	}
	// An expired one is.
	it, took, err = q.LeaseKey(ctx, "maven", "g:a", lease("other", 14, 0))
	must(t, err)
	if !took || it.LeasedBy != "other" || it.Generation != 3 {
		t.Fatalf("LeaseKey of an expired item: took=%v %+v", took, it)
	}
	// A parked one is not.
	must(t, q.Fail(ctx, "maven", "g:a", 3, store.WorkFailure{Park: true, Now: at(15)}))
	it, took, err = q.LeaseKey(ctx, "maven", "g:a", lease("r", 16, 0))
	must(t, err)
	if took || it.State != types.WorkParked {
		t.Fatalf("LeaseKey of a parked item: took=%v %+v", took, it)
	}
}

func testWorkTransactions(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	boom := errors.New("boom")
	err := s.Tx(ctx, func(ctx context.Context) error {
		must(t, q.Enqueue(ctx, "maven", "rolled-back", nil, at(0)))
		return boom
	})
	wantErr(t, err, boom)
	_, err = q.Get(ctx, "maven", "rolled-back")
	wantErr(t, err, store.ErrNotFound)
	must(t, s.Tx(ctx, func(ctx context.Context) error {
		return q.Enqueue(ctx, "maven", "committed", nil, at(0))
	}))
	_ = get[types.WorkItem](t)(q.Get(ctx, "maven", "committed"))
}

func testWorkConcurrency(t *testing.T, s store.Store) {
	ctx := context.Background()
	q := s.WorkQueue()
	const items, workers = 40, 8

	// Concurrent enqueues of one key leave one item.
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			if err := q.Enqueue(ctx, "race", "one", []byte(fmt.Sprint(i)), at(0)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if st := get[[]types.WorkStats](t)(q.Stats(ctx)); len(st) != 1 || st[0].Pending != 1 {
		t.Fatalf("concurrent enqueues of one key: %+v", st)
	}

	// Concurrent workers never take the same item.
	for i := range items {
		must(t, q.Enqueue(ctx, "bulk", fmt.Sprintf("k%02d", i), nil, at(0)))
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
					if err := q.Complete(ctx, it.Kind, it.Key, it.Generation, at(2)); err != nil {
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
	if st := get[[]types.WorkStats](t)(q.Stats(ctx)); len(st) != 1 || st[0].Kind != "race" {
		t.Fatalf("after the bulk run: %+v", st)
	}
}
