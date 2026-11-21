package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func (c cleanupStore) executions() *memtx.Table[int64, model.CleanupExecution] {
	return memtx.TableOf[int64, model.CleanupExecution](c.s.Store, "cleanup_executions")
}

func (c cleanupStore) occurrences() *memtx.Table[string, int64] {
	return memtx.TableOf[string, int64](c.s.Store, "cleanup_execution_occurrences")
}

func (c cleanupStore) CreateExecution(ctx context.Context, e model.CleanupExecution) error {
	e.Policy.ExecutionCredential = kernel.CredentialReference{}
	e.Policy.ProjectIncarnation = ""
	if err := e.Validate(); err != nil {
		return err //nolint:wrapcheck // the model names the invalid execution
	}
	return c.s.Do(ctx, func() error {
		if !c.runs().Has(e.RunID) {
			return fmt.Errorf("cleanup execution has no run: %w", kernel.ErrInvalid)
		}
		if c.executions().Has(e.RunID) || e.Occurrence != "" && c.occurrences().Has(e.Occurrence) {
			return memtx.Conflict("cleanup execution", e.RunID)
		}
		c.executions().Put(e.RunID, e)
		if e.Occurrence != "" {
			c.occurrences().Put(e.Occurrence, e.RunID)
		}
		return nil
	})
}

func (c cleanupStore) Execution(ctx context.Context, id int64) (model.CleanupExecution, error) {
	var out model.CleanupExecution
	err := c.s.Do(ctx, func() error {
		var ok bool
		out, ok = c.executions().Get(id)
		if !ok {
			return memtx.NotFound("cleanup execution", id)
		}
		return nil
	})
	return out, err
}

func (c cleanupStore) ExecutionForOccurrence(ctx context.Context, occurrence string) (model.CleanupExecution, error) {
	if occurrence == "" {
		return model.CleanupExecution{}, fmt.Errorf("a cleanup occurrence is required: %w", kernel.ErrInvalid)
	}
	var out model.CleanupExecution
	err := c.s.Do(ctx, func() error {
		id, ok := c.occurrences().Get(occurrence)
		if !ok {
			return memtx.NotFound("cleanup occurrence", occurrence)
		}
		out, _ = c.executions().Get(id)
		return nil
	})
	return out, err
}
