package inmem

import (
	"cmp"
	"context"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Archive implements store.Store.
func (s *Store) Archive() store.ArchiveStore { return archiveStore{s} }

// --- Archive ---------------------------------------------------------------

type archiveStore struct{ s *Store }

func (a archiveStore) policies() *table[string, types.ArchivePolicy] {
	return tableOf[string, types.ArchivePolicy](a.s, "archive_policies")
}

func (a archiveStore) executions() *table[int64, types.ArchiveExecution] {
	return tableOf[int64, types.ArchiveExecution](a.s, "archive_executions")
}

func (a archiveStore) CreatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	return a.s.do(ctx, func() error {
		if a.policies().has(p.Key) {
			return conflict("archive policy", p.Key)
		}
		a.policies().put(p.Key, p)
		return nil
	})
}

func (a archiveStore) Policy(ctx context.Context, key string) (types.ArchivePolicy, error) {
	var out types.ArchivePolicy
	err := a.s.do(ctx, func() error {
		var ok bool
		if out, ok = a.policies().get(key); !ok {
			return notFound("archive policy", key)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	return a.s.do(ctx, func() error {
		if !a.policies().has(p.Key) {
			return notFound("archive policy", p.Key)
		}
		a.policies().put(p.Key, p)
		return nil
	})
}

func (a archiveStore) DeletePolicy(ctx context.Context, key string) error {
	return a.s.do(ctx, func() error {
		if !a.policies().del(key) {
			return notFound("archive policy", key)
		}
		return nil
	})
}

func (a archiveStore) Policies(
	ctx context.Context,
	family types.ArchiveFamily,
) ([]types.ArchivePolicy, error) {
	var out []types.ArchivePolicy
	err := a.s.do(ctx, func() error {
		out = a.policies().all(func(_ string, v types.ArchivePolicy) bool {
			return family == "" || v.Family == family
		}, func(x, y types.ArchivePolicy) int { return strings.Compare(x.Key, y.Key) })
		return nil
	})
	return out, err
}

func (a archiveStore) CreateExecution(
	ctx context.Context,
	e types.ArchiveExecution,
) (types.ArchiveExecution, error) {
	var out types.ArchiveExecution
	err := a.s.do(ctx, func() error {
		e.ID = a.s.nextID("archive_executions")
		a.executions().put(e.ID, e)
		out, _ = a.executions().get(e.ID)
		return nil
	})
	return out, err
}

func (a archiveStore) Execution(ctx context.Context, id int64) (types.ArchiveExecution, error) {
	var out types.ArchiveExecution
	err := a.s.do(ctx, func() error {
		var ok bool
		if out, ok = a.executions().get(id); !ok {
			return notFound("archive execution", id)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdateExecution(ctx context.Context, e types.ArchiveExecution) error {
	return a.s.do(ctx, func() error {
		if !a.executions().has(e.ID) {
			return notFound("archive execution", e.ID)
		}
		a.executions().put(e.ID, e)
		return nil
	})
}

func (a archiveStore) Executions(
	ctx context.Context, f store.ExecutionFilter, p store.Page,
) ([]types.ArchiveExecution, string, error) {
	var (
		out  []types.ArchiveExecution
		next string
	)
	err := a.s.do(ctx, func() error {
		rows := a.executions().all(func(_ int64, v types.ArchiveExecution) bool {
			return (f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) &&
				(f.Kind == "" || v.Kind == f.Kind)
		}, func(x, y types.ArchiveExecution) int { return cmp.Compare(y.ID, x.ID) })
		var err error
		out, next, err = paginate(
			rows,
			p,
			true,
			func(v types.ArchiveExecution) []any { return []any{v.ID} },
		)
		return err
	})
	return out, next, err
}
