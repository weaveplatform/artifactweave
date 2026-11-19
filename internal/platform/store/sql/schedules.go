package sql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Schedules returns durable logical positions over the shared unit of work.
func (s *Store) Schedules() schedule.Store { return schedules{s} }

type schedules struct{ s *Store }

const scheduleCols = "kind, key, revision, expression, timezone, enabled, next_fire"

func scanSchedule(row interface{ Scan(...any) error }) (schedule.State, error) {
	var state schedule.State
	var next sql.NullInt64
	err := row.Scan(&state.Kind, &state.Key, &state.Revision, &state.Cron, &state.Zone, &state.Enabled, &next)
	state.Next = dialect.FromMicros(next)
	return state, err //nolint:wrapcheck // caller names the operation
}

func (s schedules) Put(ctx context.Context, state schedule.State) error {
	if err := schedule.ValidateState(state); err != nil {
		return fmt.Errorf("validate schedule state: %w", err)
	}
	_, err := s.s.Exec(ctx, "put schedule", "INSERT INTO schedules ("+scheduleCols+") VALUES (?, ?, ?, ?, ?, ?, ?) "+
		"ON CONFLICT (kind, key) DO UPDATE SET revision=excluded.revision, expression=excluded.expression, "+
		"timezone=excluded.timezone, enabled=excluded.enabled, next_fire=excluded.next_fire",
		state.Kind, state.Key, state.Revision, state.Cron, state.Zone, state.Enabled, dialect.Micros(state.Next))
	return err
}

func (s schedules) Get(ctx context.Context, ref schedule.Reference) (schedule.State, error) {
	if err := schedule.ValidateReference(ref); err != nil {
		return schedule.State{}, fmt.Errorf("validate schedule reference: %w", err)
	}
	state, err := scanSchedule(s.s.Q(ctx).QueryRowContext(ctx, s.s.Dialect().Rebind(
		"SELECT "+scheduleCols+" FROM schedules WHERE kind=? AND key=?"+s.s.LockClause(ctx)), ref.Kind, ref.Key))
	return state, s.s.Wrap("get schedule", err)
}

func (s schedules) Delete(ctx context.Context, ref schedule.Reference) error {
	if err := schedule.ValidateReference(ref); err != nil {
		return fmt.Errorf("validate schedule reference: %w", err)
	}
	return s.s.ExecOne(ctx, "delete schedule", "DELETE FROM schedules WHERE kind=? AND key=?", ref.Kind, ref.Key)
}

func (s schedules) Due(ctx context.Context, now time.Time, limit int) ([]schedule.State, error) {
	if limit <= 0 {
		limit = kernel.DefaultLimit
	}
	limit = min(limit, kernel.MaxLimit)
	var states []schedule.State
	err := s.s.Rows(ctx, "read due schedules", "SELECT "+scheduleCols+" FROM schedules "+
		"WHERE enabled=? AND next_fire IS NOT NULL AND next_fire<=? ORDER BY next_fire,kind,key LIMIT ?",
		[]any{true, dialect.Micros(now), limit}, func(rows *sql.Rows) error {
			state, err := scanSchedule(rows)
			if err != nil {
				return err
			}
			states = append(states, state)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return states, nil
}

func (s schedules) Advance(ctx context.Context, expected schedule.State, next time.Time) error {
	if !s.s.InTx(ctx) {
		return fmt.Errorf("schedule advance requires a transaction: %w", kernel.ErrInvalid)
	}
	if err := schedule.ValidateAdvance(expected, next); err != nil {
		return fmt.Errorf("validate schedule advance: %w", err)
	}
	n, err := s.s.Exec(ctx, "advance schedule", "UPDATE schedules SET next_fire=? WHERE kind=? AND key=? AND revision=? AND enabled=? AND next_fire=?",
		dialect.Micros(next), expected.Kind, expected.Key, expected.Revision, true, dialect.Micros(expected.Next))
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("schedule position changed: %w", kernel.ErrConflict)
	}
	return nil
}
