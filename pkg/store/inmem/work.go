package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// WorkQueue implements store.Store.
func (s *Store) WorkQueue() store.WorkQueue { return workQueue{s} }

type workQueue struct{ s *Store }

type workKey struct{ kind, key string }

func (w workQueue) items() *table[workKey, types.WorkItem] {
	return tableOf[workKey, types.WorkItem](w.s, "work_items")
}

// micro compares times at the precision the SQL columns store.
func micro(t time.Time) int64 { return t.UnixMicro() }

func (w workQueue) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	now time.Time,
) error {
	if err := store.ValidateWork(kind, key, payload); err != nil {
		return err
	}
	return w.s.do(ctx, func() error {
		k := workKey{kind, key}
		it, ok := w.items().get(k)
		if !ok {
			it = types.WorkItem{
				Kind: kind, Key: key, State: types.WorkPending, NotBefore: now, Enqueued: now,
			}
		}
		it.Payload = payload
		switch it.State {
		case types.WorkLeased:
			if it.DirtySince.IsZero() {
				it.DirtySince = now
			}
		case types.WorkParked:
			it.State, it.Attempts, it.NotBefore, it.Enqueued = types.WorkPending, 0, now, now
		case types.WorkPending:
		}
		it.Updated = now
		w.items().put(k, it)
		return nil
	})
}

// due reports whether it can be leased at now, and from when it was due.
func due(it types.WorkItem, now time.Time) (time.Time, bool) {
	switch it.State {
	case types.WorkPending:
		return it.NotBefore, micro(it.NotBefore) <= micro(now)
	case types.WorkLeased:
		return it.LeaseUntil, micro(it.LeaseUntil) <= micro(now)
	case types.WorkParked:
	}
	return time.Time{}, false
}

// take leases it for req.
func take(it types.WorkItem, req store.WorkLease) types.WorkItem {
	it.State, it.LeasedBy, it.LeaseUntil = types.WorkLeased, req.Holder, req.Until
	it.Generation++
	it.Attempts++
	it.DirtySince = time.Time{}
	it.Updated = req.Now
	return it
}

func (w workQueue) Lease(ctx context.Context, req store.WorkLease) ([]types.WorkItem, error) {
	if err := store.ValidateLease(req); err != nil {
		return nil, err
	}
	var out []types.WorkItem
	err := w.s.do(ctx, func() error {
		type candidate struct {
			it  types.WorkItem
			due time.Time
		}
		var cands []candidate
		for _, it := range w.items().rows {
			if len(req.Kinds) > 0 && !slices.Contains(req.Kinds, it.Kind) {
				continue
			}
			if at, ok := due(it, req.Now); ok {
				cands = append(cands, candidate{it, at})
			}
		}
		slices.SortFunc(cands, func(a, b candidate) int {
			return cmp.Or(
				cmp.Compare(micro(a.due), micro(b.due)),
				cmp.Compare(micro(a.it.Enqueued), micro(b.it.Enqueued)),
				cmp.Compare(a.it.Kind, b.it.Kind),
				cmp.Compare(a.it.Key, b.it.Key),
			)
		})
		for _, c := range cands[:min(len(cands), max(req.Limit, 0))] {
			it := take(c.it, req)
			w.items().put(workKey{it.Kind, it.Key}, it)
			out = append(out, store.Normalise(it))
		}
		return nil
	})
	return out, err
}

func (w workQueue) LeaseKey(
	ctx context.Context,
	kind, key string,
	req store.WorkLease,
) (types.WorkItem, bool, error) {
	if err := store.ValidateLease(req); err != nil {
		return types.WorkItem{}, false, err
	}
	var (
		out  types.WorkItem
		took bool
	)
	err := w.s.do(ctx, func() error {
		k := workKey{kind, key}
		it, ok := w.items().get(k)
		if !ok {
			return fmt.Errorf("work item %s/%s: %w", kind, key, store.ErrNotFound)
		}
		_, expired := due(it, req.Now)
		if it.State == types.WorkPending || it.State == types.WorkLeased && expired {
			it, took = take(it, req), true
			w.items().put(k, it)
		}
		out = store.Normalise(it)
		return nil
	})
	return out, took, err
}

// run applies fn to the item that the run with generation gen holds;
// ErrConflict when that run no longer holds it.
func (w workQueue) run(
	ctx context.Context,
	kind, key string,
	gen int64,
	fn func(it *types.WorkItem) (keep bool),
) error {
	return w.s.do(ctx, func() error {
		k := workKey{kind, key}
		it, ok := w.items().get(k)
		if !ok || it.State != types.WorkLeased || it.Generation != gen {
			return fmt.Errorf("work item %s/%s run %d: %w", kind, key, gen, store.ErrConflict)
		}
		if !fn(&it) {
			w.items().del(k)
			return nil
		}
		w.items().put(k, it)
		return nil
	})
}

// unlease makes it pending from at, folding any work asked for during the
// run into the pending one.
func unlease(it *types.WorkItem, at, now time.Time) {
	it.State, it.NotBefore, it.LeasedBy, it.LeaseUntil = types.WorkPending, at, "", time.Time{}
	it.DirtySince = time.Time{}
	it.Updated = now
}

func (w workQueue) Extend(
	ctx context.Context,
	kind, key string,
	gen int64,
	until, now time.Time,
) error {
	return w.run(ctx, kind, key, gen, func(it *types.WorkItem) bool {
		it.LeaseUntil, it.Updated = until, now
		return true
	})
}

func (w workQueue) Complete(
	ctx context.Context,
	kind, key string,
	gen int64,
	now time.Time,
) error {
	return w.run(ctx, kind, key, gen, func(it *types.WorkItem) bool {
		if it.DirtySince.IsZero() {
			return false
		}
		it.Enqueued, it.Attempts, it.LastError = it.DirtySince, 0, ""
		unlease(it, now, now)
		return true
	})
}

func (w workQueue) Fail(
	ctx context.Context,
	kind, key string,
	gen int64,
	f store.WorkFailure,
) error {
	return w.run(ctx, kind, key, gen, func(it *types.WorkItem) bool {
		it.LastError = f.Error
		if f.Park && it.DirtySince.IsZero() {
			it.State, it.NotBefore, it.LeasedBy, it.LeaseUntil = types.WorkParked, f.Now, "", time.Time{}
			it.Updated = f.Now
			return true
		}
		unlease(it, f.RetryAt, f.Now)
		return true
	})
}

func (w workQueue) Release(
	ctx context.Context,
	kind, key string,
	gen int64,
	now time.Time,
) error {
	return w.run(ctx, kind, key, gen, func(it *types.WorkItem) bool {
		it.Attempts = max(it.Attempts-1, 0)
		unlease(it, now, now)
		return true
	})
}

func (w workQueue) Get(ctx context.Context, kind, key string) (types.WorkItem, error) {
	var out types.WorkItem
	err := w.s.do(ctx, func() error {
		it, ok := w.items().get(workKey{kind, key})
		if !ok {
			return fmt.Errorf("work item %s/%s: %w", kind, key, store.ErrNotFound)
		}
		out = it
		return nil
	})
	return out, err
}

func (w workQueue) Stats(ctx context.Context) ([]types.WorkStats, error) {
	var out []types.WorkStats
	err := w.s.do(ctx, func() error {
		by := map[string]*types.WorkStats{}
		for _, it := range w.items().rows {
			st := by[it.Kind]
			if st == nil {
				st = &types.WorkStats{Kind: it.Kind}
				by[it.Kind] = st
			}
			switch it.State {
			case types.WorkPending:
				st.Pending++
				if st.OldestPending.IsZero() || it.Enqueued.Before(st.OldestPending) {
					st.OldestPending = it.Enqueued
				}
			case types.WorkLeased:
				st.Leased++
			case types.WorkParked:
				st.Parked++
			}
		}
		for _, st := range by {
			out = append(out, store.Normalise(*st))
		}
		slices.SortFunc(out, func(a, b types.WorkStats) int { return cmp.Compare(a.Kind, b.Kind) })
		return nil
	})
	return out, err
}
