package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// WorkQueue returns the persistent work queue (decision 0033).
func (s *Store) WorkQueue() work.WorkQueue { return workQueue{s} }

type workQueue struct{ s *Store }

type workKey struct{ kind, key string }

func (w workQueue) items() *memtx.Table[workKey, work.WorkItem] {
	return memtx.TableOf[workKey, work.WorkItem](w.s.Store, "work_items")
}

// micro compares times at the precision the SQL columns store.
func micro(t time.Time) int64 { return t.UnixMicro() }

func (w workQueue) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	now time.Time,
) error {
	if err := work.ValidateWork(kind, key, payload); err != nil {
		return err //nolint:wrapcheck // the store's error names the item
	}
	return w.s.Do(ctx, func() error {
		k := workKey{kind, key}
		it, ok := w.items().Get(k)
		if !ok {
			it = work.WorkItem{
				Kind: kind, Key: key, State: work.WorkPending, NotBefore: now, Enqueued: now,
			}
		}
		it.Payload = payload
		switch it.State {
		case work.WorkLeased:
			if it.DirtySince.IsZero() {
				it.DirtySince = now
			}
		case work.WorkParked:
			it.State, it.Attempts, it.NotBefore, it.Enqueued = work.WorkPending, 0, now, now
		case work.WorkPending:
		}
		it.Updated = now
		w.items().Put(k, it)
		return nil
	})
}

// due reports whether it can be leased at now, and from when it was due.
func due(it work.WorkItem, now time.Time) (time.Time, bool) {
	switch it.State {
	case work.WorkPending:
		return it.NotBefore, micro(it.NotBefore) <= micro(now)
	case work.WorkLeased:
		return it.LeaseUntil, micro(it.LeaseUntil) <= micro(now)
	case work.WorkParked:
	}
	return time.Time{}, false
}

// take leases it for req.
func take(it work.WorkItem, req work.WorkLease) work.WorkItem {
	it.State, it.LeasedBy, it.LeaseUntil = work.WorkLeased, req.Holder, req.Until
	it.Generation++
	it.Attempts++
	it.DirtySince = time.Time{}
	it.Updated = req.Now
	return it
}

func (w workQueue) Lease(ctx context.Context, req work.WorkLease) ([]work.WorkItem, error) {
	if err := work.ValidateLease(req); err != nil {
		return nil, err //nolint:wrapcheck // the store's error names the request
	}
	var out []work.WorkItem
	err := w.s.Do(ctx, func() error {
		type candidate struct {
			it  work.WorkItem
			due time.Time
		}
		var cands []candidate
		for _, it := range w.items().Rows {
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
			w.items().Put(workKey{it.Kind, it.Key}, it)
			out = append(out, kernel.Normalise(it))
		}
		return nil
	})
	return out, err
}

func (w workQueue) LeaseKey(
	ctx context.Context,
	kind, key string,
	req work.WorkLease,
) (work.WorkItem, bool, error) {
	if err := work.ValidateLease(req); err != nil {
		return work.WorkItem{}, false, err //nolint:wrapcheck // the store's error names the request
	}
	var (
		out  work.WorkItem
		took bool
	)
	err := w.s.Do(ctx, func() error {
		k := workKey{kind, key}
		it, ok := w.items().Get(k)
		if !ok {
			return fmt.Errorf("work item %s/%s: %w", kind, key, kernel.ErrNotFound)
		}
		_, expired := due(it, req.Now)
		if it.State == work.WorkPending || it.State == work.WorkLeased && expired {
			it, took = take(it, req), true
			w.items().Put(k, it)
		}
		out = kernel.Normalise(it)
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
	fn func(it *work.WorkItem) (keep bool),
) error {
	return w.s.Do(ctx, func() error {
		k := workKey{kind, key}
		it, ok := w.items().Get(k)
		if !ok || it.State != work.WorkLeased || it.Generation != gen {
			return fmt.Errorf("work item %s/%s run %d: %w", kind, key, gen, kernel.ErrConflict)
		}
		if !fn(&it) {
			w.items().Del(k)
			return nil
		}
		w.items().Put(k, it)
		return nil
	})
}

// unlease makes it pending from at, folding any work asked for during the
// run into the pending one.
func unlease(it *work.WorkItem, at, now time.Time) {
	it.State, it.NotBefore, it.LeasedBy, it.LeaseUntil = work.WorkPending, at, "", time.Time{}
	it.DirtySince = time.Time{}
	it.Updated = now
}

func (w workQueue) Extend(
	ctx context.Context,
	kind, key string,
	gen int64,
	until, now time.Time,
) error {
	return w.run(ctx, kind, key, gen, func(it *work.WorkItem) bool {
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
	return w.run(ctx, kind, key, gen, func(it *work.WorkItem) bool {
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
	f work.WorkFailure,
) error {
	return w.run(ctx, kind, key, gen, func(it *work.WorkItem) bool {
		it.LastError = f.Error
		if f.Park && it.DirtySince.IsZero() {
			it.State, it.NotBefore, it.LeasedBy, it.LeaseUntil = work.WorkParked, f.Now, "", time.Time{}
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
	return w.run(ctx, kind, key, gen, func(it *work.WorkItem) bool {
		it.Attempts = max(it.Attempts-1, 0)
		unlease(it, now, now)
		return true
	})
}

func (w workQueue) Get(ctx context.Context, kind, key string) (work.WorkItem, error) {
	var out work.WorkItem
	err := w.s.Do(ctx, func() error {
		it, ok := w.items().Get(workKey{kind, key})
		if !ok {
			return fmt.Errorf("work item %s/%s: %w", kind, key, kernel.ErrNotFound)
		}
		out = it
		return nil
	})
	return out, err
}

func (w workQueue) Stats(ctx context.Context) ([]work.WorkStats, error) {
	var out []work.WorkStats
	err := w.s.Do(ctx, func() error {
		by := map[string]*work.WorkStats{}
		for _, it := range w.items().Rows {
			st := by[it.Kind]
			if st == nil {
				st = &work.WorkStats{Kind: it.Kind}
				by[it.Kind] = st
			}
			switch it.State {
			case work.WorkPending:
				st.Pending++
				if st.OldestPending.IsZero() || it.Enqueued.Before(st.OldestPending) {
					st.OldestPending = it.Enqueued
				}
			case work.WorkLeased:
				st.Leased++
			case work.WorkParked:
				st.Parked++
			}
		}
		for _, st := range by {
			out = append(out, kernel.Normalise(*st))
		}
		slices.SortFunc(out, func(a, b work.WorkStats) int { return cmp.Compare(a.Kind, b.Kind) })
		return nil
	})
	return out, err
}
