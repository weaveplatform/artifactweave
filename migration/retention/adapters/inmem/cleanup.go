package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
	"github.com/weaveplatform/artifactweave/migration/retention/core/port"
)

// Cleanup returns the cleanup policy and run store (decision 0010).
func (s *Store) Cleanup() port.CleanupStore { return cleanupStore{s} }

type cleanupStore struct{ s *Store }

func (c cleanupStore) policies() *memtx.Table[string, model.CleanupPolicy] {
	return memtx.TableOf[string, model.CleanupPolicy](c.s.Store, "cleanup_policies")
}

func (c cleanupStore) runs() *memtx.Table[int64, model.CleanupRun] {
	return memtx.TableOf[int64, model.CleanupRun](c.s.Store, "cleanup_runs")
}

func (c cleanupStore) CreatePolicy(ctx context.Context, p model.CleanupPolicy) error {
	return c.s.Do(ctx, func() error {
		if c.policies().Has(p.Key) {
			return memtx.Conflict("cleanup policy", p.Key)
		}
		c.policies().Put(p.Key, p)
		return nil
	})
}

func (c cleanupStore) Policy(ctx context.Context, key string) (model.CleanupPolicy, error) {
	var out model.CleanupPolicy
	err := c.s.Do(ctx, func() error {
		var ok bool
		if out, ok = c.policies().Get(key); !ok {
			return memtx.NotFound("cleanup policy", key)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) UpdatePolicy(ctx context.Context, p model.CleanupPolicy) error {
	return c.s.Do(ctx, func() error {
		if !c.policies().Has(p.Key) {
			return memtx.NotFound("cleanup policy", p.Key)
		}
		c.policies().Put(p.Key, p)
		return nil
	})
}

func (c cleanupStore) DeletePolicy(ctx context.Context, key string) error {
	return c.s.Do(ctx, func() error {
		if !c.policies().Del(key) {
			return memtx.NotFound("cleanup policy", key)
		}
		return nil
	})
}

func (c cleanupStore) Policies(
	ctx context.Context,
	f port.PolicyFilter,
) ([]model.CleanupPolicy, error) {
	var out []model.CleanupPolicy
	err := c.s.Do(ctx, func() error {
		out = c.policies().All(func(_ string, p model.CleanupPolicy) bool {
			return (f.ItemType == "" || p.ItemType == f.ItemType) &&
				(f.ProjectKey == "" || p.ProjectKey == f.ProjectKey)
		}, func(a, b model.CleanupPolicy) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c cleanupStore) CreateRun(ctx context.Context, r model.CleanupRun) (model.CleanupRun, error) {
	var out model.CleanupRun
	err := c.s.Do(ctx, func() error {
		r.ID = c.s.NextID("cleanup_runs")
		c.runs().Put(r.ID, r)
		out, _ = c.runs().Get(r.ID)
		return nil
	})
	return out, err
}

func (c cleanupStore) Run(ctx context.Context, id int64) (model.CleanupRun, error) {
	var out model.CleanupRun
	err := c.s.Do(ctx, func() error {
		var ok bool
		if out, ok = c.runs().Get(id); !ok {
			return memtx.NotFound("cleanup run", id)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) UpdateRun(ctx context.Context, r model.CleanupRun) error {
	return c.s.Do(ctx, func() error {
		if !c.runs().Has(r.ID) {
			return memtx.NotFound("cleanup run", r.ID)
		}
		c.runs().Put(r.ID, r)
		return nil
	})
}

// newestFirst orders runs by descending ID.
func newestFirst(a, b model.CleanupRun) int { return cmp.Compare(b.ID, a.ID) }

func (c cleanupStore) Runs(
	ctx context.Context,
	f port.RunFilter,
	p kernel.Page,
) ([]model.CleanupRun, string, error) {
	var (
		out  []model.CleanupRun
		next string
	)
	err := c.s.Do(ctx, func() error {
		rows := c.runs().All(func(_ int64, r model.CleanupRun) bool {
			return (f.PolicyKey == "" || r.PolicyKey == f.PolicyKey) &&
				(f.ItemType == "" || r.ItemType == f.ItemType) &&
				(len(f.Statuses) == 0 || slices.Contains(f.Statuses, r.Status))
		}, newestFirst)
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			true,
			func(r model.CleanupRun) []any { return []any{r.ID} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (c cleanupStore) LastRun(ctx context.Context, policyKey string) (model.CleanupRun, error) {
	var out model.CleanupRun
	err := c.s.Do(ctx, func() error {
		rows := c.runs().
			All(func(_ int64, r model.CleanupRun) bool { return r.PolicyKey == policyKey }, newestFirst)
		if len(rows) == 0 {
			return memtx.NotFound("cleanup run of policy", policyKey)
		}
		out = rows[0]
		return nil
	})
	return out, err
}
