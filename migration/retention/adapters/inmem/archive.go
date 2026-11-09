package inmem

import (
	"cmp"
	"context"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Archive implements store.Store.
func (s *Store) Archive() store.ArchiveStore { return archiveStore{s} }

// --- Archive ---------------------------------------------------------------

type archiveStore struct{ s *Store }

func (a archiveStore) policies() *memtx.Table[string, types.ArchivePolicy] {
	return memtx.TableOf[string, types.ArchivePolicy](a.s.Store, "archive_policies")
}

func (a archiveStore) executions() *memtx.Table[int64, types.ArchiveExecution] {
	return memtx.TableOf[int64, types.ArchiveExecution](a.s.Store, "archive_executions")
}

func (a archiveStore) CreatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	return a.s.Do(ctx, func() error {
		if a.policies().Has(p.Key) {
			return memtx.Conflict("archive policy", p.Key)
		}
		a.policies().Put(p.Key, p)
		return nil
	})
}

func (a archiveStore) Policy(ctx context.Context, key string) (types.ArchivePolicy, error) {
	var out types.ArchivePolicy
	err := a.s.Do(ctx, func() error {
		var ok bool
		if out, ok = a.policies().Get(key); !ok {
			return memtx.NotFound("archive policy", key)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	return a.s.Do(ctx, func() error {
		if !a.policies().Has(p.Key) {
			return memtx.NotFound("archive policy", p.Key)
		}
		a.policies().Put(p.Key, p)
		return nil
	})
}

func (a archiveStore) DeletePolicy(ctx context.Context, key string) error {
	return a.s.Do(ctx, func() error {
		if !a.policies().Del(key) {
			return memtx.NotFound("archive policy", key)
		}
		return nil
	})
}

func (a archiveStore) Policies(
	ctx context.Context,
	family types.ArchiveFamily,
) ([]types.ArchivePolicy, error) {
	var out []types.ArchivePolicy
	err := a.s.Do(ctx, func() error {
		out = a.policies().All(func(_ string, v types.ArchivePolicy) bool {
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
	err := a.s.Do(ctx, func() error {
		e.ID = a.s.NextID("archive_executions")
		a.executions().Put(e.ID, e)
		out, _ = a.executions().Get(e.ID)
		return nil
	})
	return out, err
}

func (a archiveStore) Execution(ctx context.Context, id int64) (types.ArchiveExecution, error) {
	var out types.ArchiveExecution
	err := a.s.Do(ctx, func() error {
		var ok bool
		if out, ok = a.executions().Get(id); !ok {
			return memtx.NotFound("archive execution", id)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdateExecution(ctx context.Context, e types.ArchiveExecution) error {
	return a.s.Do(ctx, func() error {
		if !a.executions().Has(e.ID) {
			return memtx.NotFound("archive execution", e.ID)
		}
		a.executions().Put(e.ID, e)
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
	err := a.s.Do(ctx, func() error {
		rows := a.executions().All(func(_ int64, v types.ArchiveExecution) bool {
			return (f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) &&
				(f.Kind == "" || v.Kind == f.Kind)
		}, func(x, y types.ArchiveExecution) int { return cmp.Compare(y.ID, x.ID) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			true,
			func(v types.ArchiveExecution) []any { return []any{v.ID} },
		)
		return err
	})
	return out, next, err
}
