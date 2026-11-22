package inmem

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func (p executions) SelectedPaths(ctx context.Context, ref model.ExecutionReference, path string) (int64, bool, error) {
	var count int64
	var selected bool
	err := p.s.Do(ctx, func() error {
		if _, err := p.read(ref); err != nil {
			return err
		}
		paths := map[string]bool{}
		for key, effect := range p.effects().Rows {
			if key.run == ref {
				paths[effect.Path] = true
			}
		}
		count, selected = int64(len(paths)), paths[path]
		return nil
	})
	return count, selected, err //nolint:wrapcheck // store operations supply context
}

type executionEffectKey struct {
	run model.ExecutionReference
	key string
}

func (p executions) effects() *memtx.Table[executionEffectKey, model.ExecutionEffect] {
	return memtx.TableOf[executionEffectKey, model.ExecutionEffect](p.s.Store, "replication_execution_effects")
}

func (p executions) RecordEffect(ctx context.Context, input model.ExecutionEffect) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("replication effect requires a transaction: %w", kernel.ErrInvalid)
	}
	input = kernel.Normalise(input)
	return p.s.Do(ctx, func() error {
		run, err := p.read(input.Run)
		if err != nil {
			return err
		}
		key := executionEffectKey{input.Run, input.OperationKey}
		var previous *model.ExecutionEffect
		if old, ok := p.effects().Get(key); ok {
			previous = &old
		}
		next, changed, err := model.ApplyExecutionEffect(run, previous, input)
		if err != nil {
			return fmt.Errorf("record replication effect: %w", err)
		}
		if changed {
			p.effects().Put(key, input)
			p.runs().Put(run.Reference.ID, next)
		}
		return nil
	})
}

func (p executions) Effect(ctx context.Context, ref model.ExecutionReference, key string) (model.ExecutionEffect, error) {
	var out model.ExecutionEffect
	err := p.s.Do(ctx, func() error {
		run, err := p.read(ref)
		if err != nil {
			return err
		}
		var exists bool
		out, exists = p.effects().Get(executionEffectKey{ref, key})
		if !exists {
			return kernel.ErrNotFound
		}
		if out.Validate(run) != nil {
			return fmt.Errorf("stored replication effect invalid: %w", kernel.ErrStore)
		}
		return nil
	})
	if err != nil {
		return model.ExecutionEffect{}, fmt.Errorf("read replication effect: %w", err)
	}
	return out, nil
}

func effectMatches(effect model.ExecutionEffect, root string) bool {
	for _, path := range append([]string{effect.Path}, effect.Paths...) {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func (p executions) scopedOutcomes(run model.ExecutionRun, path string) (model.ExecutionOutcomes, bool) {
	if path == "" {
		return run.Outcomes, true
	}
	effects := p.effects().All(func(key executionEffectKey, e model.ExecutionEffect) bool {
		return key.run == run.Reference && effectMatches(e, path)
	}, func(a, b model.ExecutionEffect) int { return strings.Compare(a.OperationKey, b.OperationKey) })
	out := model.ExecutionOutcomes{}
	for _, effect := range effects {
		count := effect.Count()
		out.Succeeded += count.Succeeded
		out.Failed += count.Failed
		out.Skipped += count.Skipped
		out.Pending += count.Pending
	}
	return out, len(effects) != 0
}

func (p executions) PathStatus(ctx context.Context, target, incarnation, path string) (model.ExecutionPathState, error) {
	var out model.ExecutionPathState
	err := p.s.Do(ctx, func() error {
		_, configured := replicationStore(p).replications().Get(target)
		runs := p.runs().All(func(_ string, run model.ExecutionRun) bool {
			options, explicit := p.options().Get(run.Reference)
			return (configured || explicit && options.Independent) && run.Binding.TargetKey == target && run.Binding.Repository.Incarnation == incarnation
		}, func(a, b model.ExecutionRun) int {
			if order := b.Started.Compare(a.Started); order != 0 {
				return order
			}
			return strings.Compare(b.Reference.ID, a.Reference.ID)
		})
		for _, run := range runs {
			counts, found := p.scopedOutcomes(run, path)
			if !found {
				continue
			}
			if out.Latest.Started.IsZero() {
				out.Latest = model.ReplicationRun{ReplicationKey: target, Started: run.Started, Completed: run.Completed, Status: counts.Status(run.Phase)}
			}
			if run.Completed.After(out.LastCompleted) {
				out.LastCompleted = run.Completed
			}
		}
		return nil
	})
	if err != nil {
		return model.ExecutionPathState{}, fmt.Errorf("read replication path status: %w", err)
	}
	return out, nil
}
