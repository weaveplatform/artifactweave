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

// PushProgress keeps logical execution evidence independently of target rows.
func (s *Store) PushProgress() port.PushProgressStore { return pushProgress{s} }

type pushProgress struct{ s *Store }

type pushItemKey struct{ run, operation string }

func (p pushProgress) runs() *memtx.Table[string, model.PushRun] {
	return memtx.TableOf[string, model.PushRun](p.s.Store, "replication_push_runs")
}

func (p pushProgress) items() *memtx.Table[pushItemKey, model.PushItem] {
	return memtx.TableOf[pushItemKey, model.PushItem](p.s.Store, "replication_push_items")
}

func (p pushProgress) Create(ctx context.Context, id string, binding model.PushBinding, at time.Time) (model.PushRun, error) {
	candidate, err := model.NewPushRun(id, binding, at)
	if err != nil {
		return model.PushRun{}, fmt.Errorf("validate push run: %w", err)
	}
	var out model.PushRun
	err = p.s.Do(ctx, func() error {
		if current, ok := p.runs().Get(id); ok {
			if err := current.Validate(); err != nil {
				return fmt.Errorf("stored push run is invalid: %w: %w", kernel.ErrStore, err)
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
		return model.PushRun{}, fmt.Errorf("create push run: %w", err)
	}
	return out, nil
}

// run is called only under this store's Do/Tx lock, avoiding a nested Do on
// a context without a transaction binding.
func (p pushProgress) run(ref model.PushReference) (model.PushRun, error) {
	r, ok := p.runs().Get(ref.ID)
	if !ok || r.Reference != ref {
		return model.PushRun{}, kernel.ErrNotFound
	}
	if err := r.Validate(); err != nil {
		return model.PushRun{}, fmt.Errorf("stored push run is invalid: %w: %w", kernel.ErrStore, err)
	}
	return r, nil
}

func (p pushProgress) Run(ctx context.Context, ref model.PushReference) (model.PushRun, error) {
	if err := ref.Validate(); err != nil {
		return model.PushRun{}, fmt.Errorf("validate push reference: %w", err)
	}
	var out model.PushRun
	err := p.s.Do(ctx, func() error {
		var err error
		out, err = p.run(ref)
		return err
	})
	if err != nil {
		return model.PushRun{}, fmt.Errorf("read push run: %w", err)
	}
	return out, nil
}

func (p pushProgress) Advance(ctx context.Context, expected int64, next model.PushRun) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("push run advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	if err := next.Reference.Validate(); err != nil {
		return fmt.Errorf("validate push reference: %w", err)
	}
	err := p.s.Do(ctx, func() error {
		current, err := p.run(next.Reference)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
			return kernel.ErrConflict
		}
		if err != nil {
			return err
		}
		if err := model.ValidatePushAdvance(current, next); err != nil {
			return fmt.Errorf("validate push advance: %w", err)
		}
		if next.Phase != current.Phase && next.Phase != model.PushFailed {
			for _, item := range p.items().Rows {
				if item.Run == next.Reference && item.Phase != model.PushItemComplete {
					return fmt.Errorf("push traversal has unfinished items: %w", kernel.ErrConflict)
				}
			}
		}
		p.runs().Put(next.Reference.ID, next)
		return nil
	})
	if err != nil {
		return fmt.Errorf("advance push run: %w", err)
	}
	return nil
}

func (p pushProgress) CreateItem(ctx context.Context, in model.PushItem) (model.PushItem, error) {
	in = kernel.Normalise(in)
	if err := in.Run.Validate(); err != nil {
		return model.PushItem{}, fmt.Errorf("validate push reference: %w", err)
	}
	key, err := model.PushItemKey(in.Action, in.Path)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("derive push operation key: %w", err)
	}
	var out model.PushItem
	err = p.s.Do(ctx, func() error {
		run, err := p.run(in.Run)
		if err != nil {
			return err
		}
		if run.Phase.Terminal() {
			return kernel.ErrConflict
		}
		if err := in.Validate(run); err != nil {
			return fmt.Errorf("validate push intent: %w", err)
		}
		pk := pushItemKey{in.Run.ID, key}
		if current, ok := p.items().Get(pk); ok {
			if err := current.Validate(run); err != nil {
				return fmt.Errorf("stored push intent is invalid: %w: %w", kernel.ErrStore, err)
			}
			before, err := model.PushIntentFingerprint(current)
			if err != nil {
				return fmt.Errorf("fingerprint push intent: %w", err)
			}
			after, err := model.PushIntentFingerprint(in)
			if err != nil {
				return fmt.Errorf("fingerprint push intent: %w", err)
			}
			if before != after {
				return kernel.ErrConflict
			}
			out = current
			return nil
		}
		if err := model.ValidatePushItemCreation(run, in); err != nil {
			return fmt.Errorf("validate new push intent: %w", err)
		}
		p.items().Put(pk, in)
		out = kernel.Clone(in)
		return nil
	})
	if err != nil {
		return model.PushItem{}, fmt.Errorf("create push intent: %w", err)
	}
	return out, nil
}

func (p pushProgress) item(run model.PushRun, action model.PushAction, path string) (model.PushItem, error) {
	key, err := model.PushItemKey(action, path)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("derive push operation key: %w", err)
	}
	i, ok := p.items().Get(pushItemKey{run.Reference.ID, key})
	if !ok || i.Run != run.Reference || i.Action != action || i.Path != path {
		return model.PushItem{}, kernel.ErrNotFound
	}
	if err := i.Validate(run); err != nil {
		return model.PushItem{}, fmt.Errorf("stored push intent is invalid: %w: %w", kernel.ErrStore, err)
	}
	return i, nil
}

func (p pushProgress) Item(ctx context.Context, ref model.PushReference, action model.PushAction, path string) (model.PushItem, error) {
	if err := ref.Validate(); err != nil {
		return model.PushItem{}, fmt.Errorf("validate push reference: %w", err)
	}
	var out model.PushItem
	err := p.s.Do(ctx, func() error {
		run, err := p.run(ref)
		if err != nil {
			return err
		}
		out, err = p.item(run, action, path)
		return err
	})
	if err != nil {
		return model.PushItem{}, fmt.Errorf("read push intent: %w", err)
	}
	return out, nil
}

func (p pushProgress) AdvanceItem(ctx context.Context, expected int64, next model.PushItem) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("push item advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	if err := next.Run.Validate(); err != nil {
		return fmt.Errorf("validate push reference: %w", err)
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
		if err := model.ValidatePushItemAdvance(run, current, next); err != nil {
			return fmt.Errorf("validate push item advance: %w", err)
		}
		key, err := model.PushItemKey(next.Action, next.Path)
		if err != nil {
			return fmt.Errorf("derive push operation key: %w", err)
		}
		p.items().Put(pushItemKey{next.Run.ID, key}, next)
		return nil
	})
	if err != nil {
		return fmt.Errorf("advance push intent: %w", err)
	}
	return nil
}

type pushPageKeys []string

func (p pushPageKeys) Len() int           { return len(p) }
func (p pushPageKeys) Less(i, j int) bool { return p[i] > p[j] }
func (p pushPageKeys) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p *pushPageKeys) Push(value any) {
	*p = append(*p, value.(string)) //nolint:forcetypeassert // only operation-key strings are pushed through container/heap
}

func (p *pushPageKeys) Pop() any {
	last := len(*p) - 1
	value := (*p)[last]
	*p = (*p)[:last]
	return value
}

func (p pushProgress) Items(ctx context.Context, ref model.PushReference, pending bool, page kernel.Page) ([]model.PushItem, string, error) {
	if err := ref.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate push reference: %w", err)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("push item page: %w", err)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("push item continuation: %w", err)
	}
	var out []model.PushItem
	var next string
	err = p.s.Do(ctx, func() error {
		run, err := p.run(ref)
		if err != nil {
			return err
		}
		// Keep only the smallest page+1 keys; copying every source snapshot
		// while walking a large run would defeat bounded recovery reads.
		keys := make(pushPageKeys, 0, page.Limit+1)
		for key, row := range p.items().Rows {
			if row.Run != ref || pending && row.Phase == model.PushItemComplete || len(after) > 0 && key.operation <= after[0] {
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
			row, ok := p.items().Get(pushItemKey{ref.ID, key})
			if !ok {
				return kernel.ErrConflict
			}
			if err := row.Validate(run); err != nil {
				return fmt.Errorf("stored push intent is invalid: %w: %w", kernel.ErrStore, err)
			}
			out = append(out, row)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("page push intents: %w", err)
	}
	return slices.Clip(out), next, nil
}

var _ port.PushProgressStore = pushProgress{}
