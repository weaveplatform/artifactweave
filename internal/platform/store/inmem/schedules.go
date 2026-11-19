package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Schedules returns durable logical positions over the shared memory database.
func (s *Store) Schedules() schedule.Store { return schedules{s} }

type schedules struct{ s *Store }

func (s schedules) table() *memtx.Table[schedule.Reference, schedule.State] {
	return memtx.TableOf[schedule.Reference, schedule.State](s.s.Store, "schedules")
}

func (s schedules) Put(ctx context.Context, state schedule.State) error {
	if err := schedule.ValidateState(state); err != nil {
		return fmt.Errorf("validate schedule state: %w", err)
	}
	return s.s.Do(ctx, func() error { s.table().Put(state.Reference, state); return nil })
}

func (s schedules) Get(ctx context.Context, ref schedule.Reference) (schedule.State, error) {
	if err := schedule.ValidateReference(ref); err != nil {
		return schedule.State{}, fmt.Errorf("validate schedule reference: %w", err)
	}
	var state schedule.State
	err := s.s.Do(ctx, func() error {
		var ok bool
		state, ok = s.table().Get(ref)
		if !ok {
			return memtx.NotFound("schedule", ref)
		}
		return nil
	})
	return state, err
}

func (s schedules) Delete(ctx context.Context, ref schedule.Reference) error {
	if err := schedule.ValidateReference(ref); err != nil {
		return fmt.Errorf("validate schedule reference: %w", err)
	}
	return s.s.Do(ctx, func() error {
		if !s.table().Del(ref) {
			return memtx.NotFound("schedule", ref)
		}
		return nil
	})
}

func compareSchedule(a, b schedule.State) int {
	return cmp.Or(a.Next.Compare(b.Next), strings.Compare(a.Kind, b.Kind), strings.Compare(a.Key, b.Key))
}

func (s schedules) Due(ctx context.Context, now time.Time, limit int) ([]schedule.State, error) {
	limit = limitOr(limit)
	var states []schedule.State
	err := s.s.Do(ctx, func() error {
		for _, state := range s.table().Rows {
			if !state.Enabled || state.Next.IsZero() || state.Next.After(now) {
				continue
			}
			i, _ := slices.BinarySearchFunc(states, state, compareSchedule)
			if i >= limit {
				continue
			}
			states = append(states, schedule.State{})
			copy(states[i+1:], states[i:])
			states[i] = state
			if len(states) > limit {
				states = states[:limit]
			}
		}
		return nil
	})
	return states, err
}

func (s schedules) Advance(ctx context.Context, expected schedule.State, next time.Time) error {
	if !s.s.InTx(ctx) {
		return fmt.Errorf("schedule advance requires a transaction: %w", kernel.ErrInvalid)
	}
	if err := schedule.ValidateAdvance(expected, next); err != nil {
		return fmt.Errorf("validate schedule advance: %w", err)
	}
	return s.s.Do(ctx, func() error {
		current, ok := s.table().Get(expected.Reference)
		if !ok || !current.Enabled || current.Revision != expected.Revision || !current.Next.Equal(expected.Next) {
			return fmt.Errorf("schedule position changed: %w", kernel.ErrConflict)
		}
		current.Next = next
		s.table().Put(current.Reference, current)
		return nil
	})
}
