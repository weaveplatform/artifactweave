package inmem

import (
	"context"
	"fmt"
	"reflect"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// CleanupIntents persists immutable cleanup bindings and irreversible outcomes.
func (s *Store) CleanupIntents() port.CleanupIntentStore { return cleanupIntents{s} }

type cleanupIntents struct{ s *Store }

func (p cleanupIntents) rows() *memtx.Table[string, model.CleanupIntent] {
	return memtx.TableOf[string, model.CleanupIntent](p.s.Store, "federated_cleanup_intents")
}

func (p cleanupIntents) Read(ctx context.Context, id string) (model.CleanupIntent, error) {
	var out model.CleanupIntent
	err := p.s.Do(ctx, func() error {
		value, found := p.rows().Get(id)
		if !found {
			return memtx.NotFound("cleanup operation", id)
		}
		if value.Validate() != nil {
			return fmt.Errorf("stored cleanup operation is invalid: %w", kernel.ErrStore)
		}
		out = kernel.Clone(value)
		return nil
	})
	return out, err
}

func (p cleanupIntents) Create(ctx context.Context, value model.CleanupIntent) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("cleanup operation admission requires a transaction: %w", kernel.ErrInvalid)
	}
	value = kernel.Normalise(value)
	if err := value.Validate(); err != nil {
		return fmt.Errorf("persist cleanup operation: %w", err)
	}
	if value.State != model.CleanupPreparing || value.Revision != 1 {
		return fmt.Errorf("cleanup operation must begin preparing: %w", kernel.ErrInvalid)
	}
	return p.s.Do(ctx, func() error {
		if current, exists := p.rows().Get(value.ID); exists {
			if current.RunID != value.RunID || current.FileID != value.FileID || !reflect.DeepEqual(current.Selected, value.Selected) {
				return fmt.Errorf("cleanup operation identity was already bound: %w", kernel.ErrConflict)
			}
			return nil
		}
		p.rows().Put(value.ID, kernel.Clone(value))
		return nil
	})
}

func (p cleanupIntents) Advance(ctx context.Context, expected int64, next model.CleanupIntent) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("cleanup operation publication requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	return p.s.Do(ctx, func() error {
		current, found := p.rows().Get(next.ID)
		if !found || current.Revision != expected {
			return fmt.Errorf("cleanup operation owner changed: %w", kernel.ErrConflict)
		}
		if err := model.ValidateCleanupIntentAdvance(current, next); err != nil {
			return fmt.Errorf("persist cleanup operation: %w", err)
		}
		p.rows().Put(next.ID, kernel.Clone(next))
		return nil
	})
}
