package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func (p executions) options() *memtx.Table[model.ExecutionReference, model.ExecutionOptions] {
	return memtx.TableOf[model.ExecutionReference, model.ExecutionOptions](p.s.Store, "replication_execution_options")
}

func (p executions) Options(ctx context.Context, ref model.ExecutionReference) (model.ExecutionOptions, error) {
	if err := ref.Validate(); err != nil {
		return model.ExecutionOptions{}, fmt.Errorf("validate execution options reference: %w", err)
	}
	var out model.ExecutionOptions
	err := p.s.Do(ctx, func() error {
		if _, err := p.read(ref); err != nil {
			return err
		}
		var exists bool
		out, exists = p.options().Get(ref)
		if !exists {
			return kernel.ErrNotFound
		}
		if out.Validate() != nil {
			return fmt.Errorf("stored execution options invalid: %w", kernel.ErrStore)
		}
		return nil
	})
	return out, err //nolint:wrapcheck // operations supply store context
}

func (p executions) PutOptions(ctx context.Context, ref model.ExecutionReference, options model.ExecutionOptions) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("execution options require admission transaction: %w", kernel.ErrInvalid)
	}
	if err := options.Validate(); err != nil {
		return fmt.Errorf("validate execution options: %w", err)
	}
	options = kernel.Normalise(options)
	return p.s.Do(ctx, func() error {
		run, err := p.read(ref)
		if err != nil {
			return err
		}
		if !run.Binding.OneShot {
			return kernel.ErrConflict
		}
		if current, exists := p.options().Get(ref); exists {
			if current != options {
				return kernel.ErrConflict
			}
			return nil
		}
		if run.Phase != model.ExecutionPending {
			return kernel.ErrConflict
		}
		p.options().Put(ref, options)
		return nil
	})
}
