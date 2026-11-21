package inmem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Executions retains dispatch lifetimes independently of queue/config rows.
func (s *Store) Executions() port.ExecutionProgressStore { return executions{s} }

type executions struct{ s *Store }

func (p executions) runs() *memtx.Table[string, model.ExecutionRun] {
	return memtx.TableOf[string, model.ExecutionRun](p.s.Store, "replication_executions")
}

func (p executions) occurrences() *memtx.Table[string, model.ExecutionReference] {
	return memtx.TableOf[string, model.ExecutionReference](p.s.Store, "replication_execution_occurrences")
}

func (p executions) Create(ctx context.Context, id string, binding model.ExecutionBinding, at time.Time) (model.ExecutionRun, error) {
	candidate, err := model.NewExecutionRun(id, binding, at)
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("validate execution admission: %w", err)
	}
	var out model.ExecutionRun
	err = p.s.Do(ctx, func() error {
		if current, exists := p.runs().Get(id); exists {
			if err := current.Validate(); err != nil {
				return fmt.Errorf("stored replication execution invalid: %w", kernel.ErrStore)
			}
			if !reflect.DeepEqual(current.Binding, candidate.Binding) {
				return kernel.ErrConflict
			}
			out = current
			return nil
		}
		if binding.Occurrence != "" {
			if _, exists := p.occurrences().Get(binding.Occurrence); exists {
				return kernel.ErrConflict
			}
			p.occurrences().Put(binding.Occurrence, candidate.Reference)
		}
		p.runs().Put(id, candidate)
		out = kernel.Clone(candidate)
		return nil
	})
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("create replication execution: %w", err)
	}
	return out, nil
}

func (p executions) read(ref model.ExecutionReference) (model.ExecutionRun, error) {
	run, exists := p.runs().Get(ref.ID)
	if !exists || run.Reference != ref {
		return model.ExecutionRun{}, kernel.ErrNotFound
	}
	if err := run.Validate(); err != nil {
		return model.ExecutionRun{}, fmt.Errorf("stored replication execution invalid: %w", kernel.ErrStore)
	}
	return run, nil
}

func (p executions) Run(ctx context.Context, ref model.ExecutionReference) (model.ExecutionRun, error) {
	if err := ref.Validate(); err != nil {
		return model.ExecutionRun{}, fmt.Errorf("validate execution reference: %w", err)
	}
	var out model.ExecutionRun
	err := p.s.Do(ctx, func() error {
		var err error
		out, err = p.read(ref)
		return err
	})
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("read replication execution: %w", err)
	}
	return out, nil
}

func (p executions) ForOccurrence(ctx context.Context, occurrence string) (model.ExecutionRun, error) {
	if occurrence == "" {
		return model.ExecutionRun{}, kernel.ErrInvalid
	}
	var out model.ExecutionRun
	err := p.s.Do(ctx, func() error {
		ref, exists := p.occurrences().Get(occurrence)
		if !exists {
			return kernel.ErrNotFound
		}
		var err error
		out, err = p.read(ref)
		return err
	})
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("read replication occurrence: %w", err)
	}
	return out, nil
}

func (p executions) Advance(ctx context.Context, expected int64, next model.ExecutionRun) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("execution advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	return p.s.Do(ctx, func() error {
		current, err := p.read(next.Reference)
		if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
			return kernel.ErrConflict
		}
		if err != nil {
			return err
		}
		if err := model.ValidateExecutionAdvance(current, next); err != nil {
			return fmt.Errorf("validate execution advance: %w", err)
		}
		p.runs().Put(next.Reference.ID, next)
		return nil
	})
}
