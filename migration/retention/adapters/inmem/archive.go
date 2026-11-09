package inmem

import (
	"cmp"
	"context"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
	"github.com/weaveplatform/artifactweave/migration/retention/core/port"
)

// Archive returns the archive policy and execution store (decision
// 0024).
func (s *Store) Archive() port.ArchiveStore { return archiveStore{s} }

type archiveStore struct{ s *Store }

func (a archiveStore) policies() *memtx.Table[string, model.ArchivePolicy] {
	return memtx.TableOf[string, model.ArchivePolicy](a.s.Store, "archive_policies")
}

func (a archiveStore) executions() *memtx.Table[int64, model.ArchiveExecution] {
	return memtx.TableOf[int64, model.ArchiveExecution](a.s.Store, "archive_executions")
}

func (a archiveStore) CreatePolicy(ctx context.Context, p model.ArchivePolicy) error {
	return a.s.Do(ctx, func() error {
		if a.policies().Has(p.Key) {
			return memtx.Conflict("archive policy", p.Key)
		}
		a.policies().Put(p.Key, p)
		return nil
	})
}

func (a archiveStore) Policy(ctx context.Context, key string) (model.ArchivePolicy, error) {
	var out model.ArchivePolicy
	err := a.s.Do(ctx, func() error {
		var ok bool
		if out, ok = a.policies().Get(key); !ok {
			return memtx.NotFound("archive policy", key)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdatePolicy(ctx context.Context, p model.ArchivePolicy) error {
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
	family model.ArchiveFamily,
) ([]model.ArchivePolicy, error) {
	var out []model.ArchivePolicy
	err := a.s.Do(ctx, func() error {
		out = a.policies().All(func(_ string, v model.ArchivePolicy) bool {
			return family == "" || v.Family == family
		}, func(x, y model.ArchivePolicy) int { return strings.Compare(x.Key, y.Key) })
		return nil
	})
	return out, err
}

func (a archiveStore) CreateExecution(
	ctx context.Context,
	e model.ArchiveExecution,
) (model.ArchiveExecution, error) {
	var out model.ArchiveExecution
	err := a.s.Do(ctx, func() error {
		e.ID = a.s.NextID("archive_executions")
		a.executions().Put(e.ID, e)
		out, _ = a.executions().Get(e.ID)
		return nil
	})
	return out, err
}

func (a archiveStore) Execution(ctx context.Context, id int64) (model.ArchiveExecution, error) {
	var out model.ArchiveExecution
	err := a.s.Do(ctx, func() error {
		var ok bool
		if out, ok = a.executions().Get(id); !ok {
			return memtx.NotFound("archive execution", id)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdateExecution(ctx context.Context, e model.ArchiveExecution) error {
	return a.s.Do(ctx, func() error {
		if !a.executions().Has(e.ID) {
			return memtx.NotFound("archive execution", e.ID)
		}
		a.executions().Put(e.ID, e)
		return nil
	})
}

func (a archiveStore) Executions(
	ctx context.Context, f port.ExecutionFilter, p kernel.Page,
) ([]model.ArchiveExecution, string, error) {
	var (
		out  []model.ArchiveExecution
		next string
	)
	err := a.s.Do(ctx, func() error {
		rows := a.executions().All(func(_ int64, v model.ArchiveExecution) bool {
			return (f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) &&
				(f.Kind == "" || v.Kind == f.Kind)
		}, func(x, y model.ArchiveExecution) int { return cmp.Compare(y.ID, x.ID) })
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			true,
			func(v model.ArchiveExecution) []any { return []any{v.ID} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}
