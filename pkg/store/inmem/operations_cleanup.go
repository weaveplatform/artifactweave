package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Cleanup implements store.Store.
func (s *Store) Cleanup() store.CleanupStore { return cleanupStore{s} }

type cleanupStore struct{ s *Store }

func (c cleanupStore) policies() *table[string, types.CleanupPolicy] {
	return tableOf[string, types.CleanupPolicy](c.s, "cleanup_policies")
}

func (c cleanupStore) runs() *table[int64, types.CleanupRun] {
	return tableOf[int64, types.CleanupRun](c.s, "cleanup_runs")
}

func (c cleanupStore) CreatePolicy(ctx context.Context, p types.CleanupPolicy) error {
	return c.s.do(ctx, func() error {
		if c.policies().has(p.Key) {
			return conflict("cleanup policy", p.Key)
		}
		c.policies().put(p.Key, p)
		return nil
	})
}

func (c cleanupStore) Policy(ctx context.Context, key string) (types.CleanupPolicy, error) {
	var out types.CleanupPolicy
	err := c.s.do(ctx, func() error {
		var ok bool
		if out, ok = c.policies().get(key); !ok {
			return notFound("cleanup policy", key)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) UpdatePolicy(ctx context.Context, p types.CleanupPolicy) error {
	return c.s.do(ctx, func() error {
		if !c.policies().has(p.Key) {
			return notFound("cleanup policy", p.Key)
		}
		c.policies().put(p.Key, p)
		return nil
	})
}

func (c cleanupStore) DeletePolicy(ctx context.Context, key string) error {
	return c.s.do(ctx, func() error {
		if !c.policies().del(key) {
			return notFound("cleanup policy", key)
		}
		return nil
	})
}

func (c cleanupStore) Policies(
	ctx context.Context,
	f store.PolicyFilter,
) ([]types.CleanupPolicy, error) {
	var out []types.CleanupPolicy
	err := c.s.do(ctx, func() error {
		out = c.policies().all(func(_ string, p types.CleanupPolicy) bool {
			return (f.ItemType == "" || p.ItemType == f.ItemType) &&
				(f.ProjectKey == "" || p.ProjectKey == f.ProjectKey)
		}, func(a, b types.CleanupPolicy) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (c cleanupStore) CreateRun(ctx context.Context, r types.CleanupRun) (types.CleanupRun, error) {
	var out types.CleanupRun
	err := c.s.do(ctx, func() error {
		r.ID = c.s.nextID("cleanup_runs")
		c.runs().put(r.ID, r)
		out, _ = c.runs().get(r.ID)
		return nil
	})
	return out, err
}

func (c cleanupStore) Run(ctx context.Context, id int64) (types.CleanupRun, error) {
	var out types.CleanupRun
	err := c.s.do(ctx, func() error {
		var ok bool
		if out, ok = c.runs().get(id); !ok {
			return notFound("cleanup run", id)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) UpdateRun(ctx context.Context, r types.CleanupRun) error {
	return c.s.do(ctx, func() error {
		if !c.runs().has(r.ID) {
			return notFound("cleanup run", r.ID)
		}
		c.runs().put(r.ID, r)
		return nil
	})
}

// newestFirst orders runs by descending ID.
func newestFirst(a, b types.CleanupRun) int { return cmp.Compare(b.ID, a.ID) }

func (c cleanupStore) Runs(
	ctx context.Context,
	f store.RunFilter,
	p store.Page,
) ([]types.CleanupRun, string, error) {
	var (
		out  []types.CleanupRun
		next string
	)
	err := c.s.do(ctx, func() error {
		rows := c.runs().all(func(_ int64, r types.CleanupRun) bool {
			return (f.PolicyKey == "" || r.PolicyKey == f.PolicyKey) &&
				(f.ItemType == "" || r.ItemType == f.ItemType) &&
				(len(f.Statuses) == 0 || slices.Contains(f.Statuses, r.Status))
		}, newestFirst)
		var err error
		out, next, err = paginate(
			rows,
			p,
			true,
			func(r types.CleanupRun) []any { return []any{r.ID} },
		)
		return err
	})
	return out, next, err
}

func (c cleanupStore) LastRun(ctx context.Context, policyKey string) (types.CleanupRun, error) {
	var out types.CleanupRun
	err := c.s.do(ctx, func() error {
		rows := c.runs().
			all(func(_ int64, r types.CleanupRun) bool { return r.PolicyKey == policyKey }, newestFirst)
		if len(rows) == 0 {
			return notFound("cleanup run of policy", policyKey)
		}
		out = rows[0]
		return nil
	})
	return out, err
}
