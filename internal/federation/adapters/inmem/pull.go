package inmem

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// PullProgress keeps logical execution evidence independently of target rows.
func (s *Store) PullProgress() port.PullProgressStore { return pullProgress{s} }

type pullProgress struct{ s *Store }

type pullItemKey struct{ run, operation string }

func (p pullProgress) runs() *memtx.Table[string, model.PullRun] {
	return memtx.TableOf[string, model.PullRun](p.s.Store, "replication_pull_runs")
}

func (p pullProgress) items() *memtx.Table[pullItemKey, model.PullItem] {
	return memtx.TableOf[pullItemKey, model.PullItem](p.s.Store, "replication_pull_items")
}

// order holds each intent's recorded position, as the SQL ordinal column does.
func (p pullProgress) order() *memtx.Table[pullItemKey, int64] {
	return memtx.TableOf[pullItemKey, int64](p.s.Store, "replication_pull_item_order")
}

func (p pullProgress) Create(ctx context.Context, id string, binding model.PullBinding, at time.Time) (model.PullRun, error) {
	candidate, err := model.NewPullRun(id, binding, at)
	if err != nil {
		return model.PullRun{}, fmt.Errorf("validate pull run: %w", err)
	}
	var out model.PullRun
	err = p.s.Do(ctx, func() error {
		if current, ok := p.runs().Get(id); ok {
			if err := current.Validate(); err != nil {
				return fmt.Errorf("stored pull run is invalid: %w: %w", kernel.ErrStore, err)
			}
			if current.Binding != candidate.Binding {
				return kernel.ErrConflict
			}
			out = current
			return nil
		}
		p.runs().Put(id, candidate)
		out = kernel.Clone(candidate)
		return nil
	})
	if err != nil {
		return model.PullRun{}, fmt.Errorf("create pull run: %w", err)
	}
	return out, nil
}

// run is called only under this store's Do/Tx lock, avoiding a nested Do on
// a context without a transaction binding.
func (p pullProgress) run(ref model.PullReference) (model.PullRun, error) {
	r, ok := p.runs().Get(ref.ID)
	if !ok || r.Reference != ref {
		return model.PullRun{}, kernel.ErrNotFound
	}
	if err := r.Validate(); err != nil {
		return model.PullRun{}, fmt.Errorf("stored pull run is invalid: %w: %w", kernel.ErrStore, err)
	}
	return r, nil
}

func (p pullProgress) Run(ctx context.Context, ref model.PullReference) (model.PullRun, error) {
	if err := ref.Validate(); err != nil {
		return model.PullRun{}, fmt.Errorf("validate pull reference: %w", err)
	}
	var out model.PullRun
	err := p.s.Do(ctx, func() error {
		var err error
		out, err = p.run(ref)
		return err
	})
	if err != nil {
		return model.PullRun{}, fmt.Errorf("read pull run: %w", err)
	}
	return out, nil
}

func (p pullProgress) Advance(ctx context.Context, expected int64, next model.PullRun) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("pull run advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	if err := next.Reference.Validate(); err != nil {
		return fmt.Errorf("validate pull reference: %w", err)
	}
	err := p.s.Do(ctx, func() error {
		current, err := p.run(next.Reference)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
			return kernel.ErrConflict
		}
		if err != nil {
			return err
		}
		if err := model.ValidatePullAdvance(current, next); err != nil {
			return fmt.Errorf("validate pull advance: %w", err)
		}
		if next.Phase != current.Phase && next.Phase != model.PullFailed {
			for _, item := range p.items().Rows {
				if item.Run == next.Reference && item.Phase != model.PullItemComplete {
					return fmt.Errorf("pull traversal has unfinished items: %w", kernel.ErrConflict)
				}
			}
		}
		p.runs().Put(next.Reference.ID, next)
		return nil
	})
	if err != nil {
		return fmt.Errorf("advance pull run: %w", err)
	}
	return nil
}

func (p pullProgress) CreateItem(ctx context.Context, in model.PullItem) (model.PullItem, error) {
	in = kernel.Normalise(in)
	if err := in.Run.Validate(); err != nil {
		return model.PullItem{}, fmt.Errorf("validate pull reference: %w", err)
	}
	key, err := model.PullItemKey(in.Action, in.Path)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("derive pull operation key: %w", err)
	}
	var out model.PullItem
	err = p.s.Do(ctx, func() error {
		run, err := p.run(in.Run)
		if err != nil {
			return err
		}
		if run.Phase.Terminal() {
			return kernel.ErrConflict
		}
		if err := in.Validate(run); err != nil {
			return fmt.Errorf("validate pull intent: %w", err)
		}
		pk := pullItemKey{in.Run.ID, key}
		if current, ok := p.items().Get(pk); ok {
			if err := current.Validate(run); err != nil {
				return fmt.Errorf("stored pull intent is invalid: %w: %w", kernel.ErrStore, err)
			}
			before, err := model.PullIntentFingerprint(current)
			if err != nil {
				return fmt.Errorf("fingerprint pull intent: %w", err)
			}
			after, err := model.PullIntentFingerprint(in)
			if err != nil {
				return fmt.Errorf("fingerprint pull intent: %w", err)
			}
			if before != after {
				return kernel.ErrConflict
			}
			out = current
			return nil
		}
		if err := model.ValidatePullItemCreation(run, in); err != nil {
			return fmt.Errorf("validate new pull intent: %w", err)
		}
		var last int64
		for key, ordinal := range p.order().Rows {
			if key.run == in.Run.ID && ordinal > last {
				last = ordinal
			}
		}
		p.items().Put(pk, in)
		p.order().Put(pk, last+1)
		out = kernel.Clone(in)
		return nil
	})
	if err != nil {
		return model.PullItem{}, fmt.Errorf("create pull intent: %w", err)
	}
	return out, nil
}

func (p pullProgress) item(run model.PullRun, action model.PullAction, path string) (model.PullItem, error) {
	key, err := model.PullItemKey(action, path)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("derive pull operation key: %w", err)
	}
	i, ok := p.items().Get(pullItemKey{run.Reference.ID, key})
	if !ok || i.Run != run.Reference || i.Action != action || i.Path != path {
		return model.PullItem{}, kernel.ErrNotFound
	}
	if err := i.Validate(run); err != nil {
		return model.PullItem{}, fmt.Errorf("stored pull intent is invalid: %w: %w", kernel.ErrStore, err)
	}
	return i, nil
}

func (p pullProgress) Item(ctx context.Context, ref model.PullReference, action model.PullAction, path string) (model.PullItem, error) {
	if err := ref.Validate(); err != nil {
		return model.PullItem{}, fmt.Errorf("validate pull reference: %w", err)
	}
	var out model.PullItem
	err := p.s.Do(ctx, func() error {
		run, err := p.run(ref)
		if err != nil {
			return err
		}
		out, err = p.item(run, action, path)
		return err
	})
	if err != nil {
		return model.PullItem{}, fmt.Errorf("read pull intent: %w", err)
	}
	return out, nil
}

func (p pullProgress) AdvanceItem(ctx context.Context, expected int64, next model.PullItem) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("pull item advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	if err := next.Run.Validate(); err != nil {
		return fmt.Errorf("validate pull reference: %w", err)
	}
	err := p.s.Do(ctx, func() error {
		run, err := p.run(next.Run)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.ErrConflict
		}
		if err != nil {
			return err
		}
		current, err := p.item(run, next.Action, next.Path)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
			return kernel.ErrConflict
		}
		if err != nil {
			return err
		}
		if err := model.ValidatePullItemAdvance(run, current, next); err != nil {
			return fmt.Errorf("validate pull item advance: %w", err)
		}
		key, err := model.PullItemKey(next.Action, next.Path)
		if err != nil {
			return fmt.Errorf("derive pull operation key: %w", err)
		}
		p.items().Put(pullItemKey{next.Run.ID, key}, next)
		return nil
	})
	if err != nil {
		return fmt.Errorf("advance pull intent: %w", err)
	}
	return nil
}

type pullPageKeys []string

func (p pullPageKeys) Len() int           { return len(p) }
func (p pullPageKeys) Less(i, j int) bool { return p[i] > p[j] }
func (p pullPageKeys) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p *pullPageKeys) Push(value any) {
	*p = append(*p, value.(string)) //nolint:forcetypeassert // only operation-key strings are pulled through container/heap
}

func (p *pullPageKeys) Pop() any {
	last := len(*p) - 1
	value := (*p)[last]
	*p = (*p)[:last]
	return value
}

func (p pullProgress) NextPending(ctx context.Context, ref model.PullReference) (model.PullItem, bool, error) {
	if err := ref.Validate(); err != nil {
		return model.PullItem{}, false, fmt.Errorf("validate pull reference: %w", err)
	}
	var out model.PullItem
	found := false
	err := p.s.Do(ctx, func() error {
		run, err := p.run(ref)
		if err != nil {
			return err
		}
		var best pullItemKey
		var bestOrdinal int64
		for key, row := range p.items().Rows {
			if row.Run != ref || row.Phase == model.PullItemComplete {
				continue
			}
			ordinal, _ := p.order().Get(key)
			if !found || ordinal < bestOrdinal || ordinal == bestOrdinal && key.operation < best.operation {
				best, bestOrdinal, found = key, ordinal, true
			}
		}
		if !found {
			return nil
		}
		item, _ := p.items().Get(best)
		if err := item.Validate(run); err != nil {
			return fmt.Errorf("stored pull intent is invalid: %w: %w", kernel.ErrStore, err)
		}
		out = kernel.Clone(item)
		return nil
	})
	if err != nil {
		return model.PullItem{}, false, fmt.Errorf("read next pull intent: %w", err)
	}
	return out, found, nil
}

func (p pullProgress) Items(ctx context.Context, ref model.PullReference, pending bool, page kernel.Page) ([]model.PullItem, string, error) {
	if err := ref.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate pull reference: %w", err)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("pull item page: %w", err)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("pull item continuation: %w", err)
	}
	var out []model.PullItem
	var next string
	err = p.s.Do(ctx, func() error {
		run, err := p.run(ref)
		if err != nil {
			return err
		}
		// Keep only the smallest page+1 keys; copying every source snapshot
		// while walking a large run would defeat bounded recovery reads.
		keys := make(pullPageKeys, 0, page.Limit+1)
		for key, row := range p.items().Rows {
			if row.Run != ref || pending && row.Phase == model.PullItemComplete || len(after) > 0 && key.operation <= after[0] {
				continue
			}
			if len(keys) < page.Limit+1 {
				heap.Push(&keys, key.operation)
			} else if key.operation < keys[0] {
				keys[0] = key.operation
				heap.Fix(&keys, 0)
			}
		}
		ordered := make([]string, len(keys))
		for i := len(ordered) - 1; i >= 0; i-- {
			ordered[i] = heap.Pop(&keys).(string) //nolint:forcetypeassert // the heap stores only operation-key strings
		}
		if len(ordered) > page.Limit {
			ordered = ordered[:page.Limit]
			next = kernel.Cursor(ordered[len(ordered)-1])
		}
		for _, key := range ordered {
			row, ok := p.items().Get(pullItemKey{ref.ID, key})
			if !ok {
				return kernel.ErrConflict
			}
			if err := row.Validate(run); err != nil {
				return fmt.Errorf("stored pull intent is invalid: %w: %w", kernel.ErrStore, err)
			}
			out = append(out, row)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("page pull intents: %w", err)
	}
	return slices.Clip(out), next, nil
}

var _ port.PullProgressStore = pullProgress{}
