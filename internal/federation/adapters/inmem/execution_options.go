package inmem

import (
	"context"
	"fmt"
	"sort"

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
		run, err := p.read(ref)
		if err != nil {
			return err
		}
		var exists bool
		out, exists = p.options().Get(ref)
		if !exists {
			return kernel.ErrNotFound
		}
		if out.ValidateBinding(run) != nil {
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
		if err := options.ValidateBinding(run); err != nil {
			return fmt.Errorf("bind execution options: %w", err)
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

func (p executions) ExplicitTargets(ctx context.Context, repository, incarnation string, page kernel.Page) ([]model.ReplicationTargetState, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("explicit target page: %w", err)
	}
	key, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("explicit target cursor: %w", err)
	}
	var out []model.ReplicationTargetState
	err = p.s.Do(ctx, func() error {
		seen := map[string]string{}
		for ref, options := range p.options().Rows {
			if !options.Independent {
				continue
			}
			run, exists := p.runs().Get(ref.ID)
			if !exists || run.Reference != ref || run.Binding.Repository.Key != repository || run.Binding.Repository.Incarnation != incarnation ||
				len(key) > 0 && run.Binding.TargetKey <= key[0] {
				continue
			}
			seen[run.Binding.TargetKey] = options.URL
		}
		for target, url := range seen {
			out = append(out, model.ReplicationTargetState{Key: target, URL: url})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		if len(out) > page.Limit+1 {
			out = out[:page.Limit+1]
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("read explicit replication targets: %w", err)
	}
	out, next := kernel.Trim(out, page.Limit, func(target model.ReplicationTargetState) []string { return []string{target.Key} })
	return out, next, nil
}
