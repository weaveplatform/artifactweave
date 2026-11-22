package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Executions retains dispatch lifetimes independently of queue/config rows.
func (s *Store) Executions() port.ExecutionProgressStore { return executions{s} }

type executions struct{ s *Store }

const executionColumns = "run_id, incarnation, target_key, repo_key, repo_incarnation, direction, occurrence, " +
	"binding_json, version, phase, failure_reason, succeeded, failed, skipped, pending, started_at, updated_at, completed_at"

func scanExecution(row interface{ Scan(...any) error }) (model.ExecutionRun, error) {
	var run model.ExecutionRun
	var target, repository, incarnation, direction, document string
	var occurrence sql.NullString
	var started, updated, completed sql.NullInt64
	if err := row.Scan(&run.Reference.ID, &run.Reference.Incarnation, &target, &repository, &incarnation,
		&direction, &occurrence, &document, &run.Version, &run.Phase, &run.Failure, &run.Outcomes.Succeeded, &run.Outcomes.Failed,
		&run.Outcomes.Skipped, &run.Outcomes.Pending, &started, &updated, &completed); err != nil {
		return model.ExecutionRun{}, err //nolint:wrapcheck // store supplies driver/scan context
	}
	if err := dialect.FromJSON(sql.NullString{String: document, Valid: true}, &run.Binding); err != nil {
		return model.ExecutionRun{}, fmt.Errorf("stored execution binding invalid: %w", kernel.ErrStore)
	}
	run.Started, run.Updated, run.Completed = dialect.FromMicros(started), dialect.FromMicros(updated), dialect.FromMicros(completed)
	if target != run.Binding.TargetKey || repository != run.Binding.Repository.Key || incarnation != run.Binding.Repository.Incarnation ||
		direction != string(run.Binding.Direction) || occurrence.String != run.Binding.Occurrence || run.Validate() != nil {
		return model.ExecutionRun{}, fmt.Errorf("stored execution provenance invalid: %w", kernel.ErrStore)
	}
	return run, nil
}

func (p executions) read(ctx context.Context, column, value string) (model.ExecutionRun, error) {
	// Only the two owner-selected identity columns enter SQL, never caller text.
	if column != "run_id" && column != "occurrence" {
		return model.ExecutionRun{}, kernel.ErrInvalid
	}
	run, err := scanExecution(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+executionColumns+" FROM replication_executions WHERE "+column+"=?"+p.s.LockClause(ctx)), value))
	return run, p.s.Wrap("read replication execution", err)
}

func (p executions) Create(ctx context.Context, id string, binding model.ExecutionBinding, at time.Time) (model.ExecutionRun, error) {
	candidate, err := model.NewExecutionRun(id, binding, at)
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("validate execution admission: %w", err)
	}
	document, err := dialect.ToJSON(candidate.Binding)
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("encode execution binding: %w", err)
	}
	var out model.ExecutionRun
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		_, err := p.s.Exec(ctx, "claim replication execution",
			dialect.InsertIgnore("replication_executions", dialect.OpsCols(executionColumns), []string{"run_id"}),
			id, candidate.Reference.Incarnation, binding.TargetKey, binding.Repository.Key, binding.Repository.Incarnation,
			binding.Direction, dialect.NullString(binding.Occurrence), document, candidate.Version, candidate.Phase, candidate.Failure,
			candidate.Outcomes.Succeeded, candidate.Outcomes.Failed, candidate.Outcomes.Skipped, candidate.Outcomes.Pending,
			dialect.Micros(candidate.Started), dialect.Micros(candidate.Updated), dialect.Micros(candidate.Completed))
		if err != nil {
			return err
		}
		current, err := p.read(ctx, "run_id", id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current.Binding, candidate.Binding) {
			return kernel.ErrConflict
		}
		out = current
		return nil
	})
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("create replication execution: %w", err)
	}
	return out, nil
}

func (p executions) Run(ctx context.Context, ref model.ExecutionReference) (model.ExecutionRun, error) {
	if err := ref.Validate(); err != nil {
		return model.ExecutionRun{}, fmt.Errorf("validate execution reference: %w", err)
	}
	run, err := p.read(ctx, "run_id", ref.ID)
	if err != nil {
		return model.ExecutionRun{}, err
	}
	if run.Reference != ref {
		return model.ExecutionRun{}, kernel.ErrNotFound
	}
	return run, nil
}

func (p executions) ForOccurrence(ctx context.Context, occurrence string) (model.ExecutionRun, error) {
	if occurrence == "" {
		return model.ExecutionRun{}, kernel.ErrInvalid
	}
	return p.read(ctx, "occurrence", occurrence)
}

func (p executions) Advance(ctx context.Context, expected int64, next model.ExecutionRun) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("execution advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	current, err := p.Run(ctx, next.Reference)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
		return kernel.ErrConflict
	}
	if err != nil {
		return err
	}
	if err := model.ValidateExecutionAdvance(current, next); err != nil {
		return fmt.Errorf("validate execution advance: %w", err)
	}
	count, err := p.s.Exec(ctx, "advance replication execution",
		"UPDATE replication_executions SET version=?,phase=?,failure_reason=?,succeeded=?,failed=?,skipped=?,pending=?,updated_at=?,completed_at=? "+
			"WHERE run_id=? AND incarnation=? AND version=?", next.Version, next.Phase, next.Failure, next.Outcomes.Succeeded,
		next.Outcomes.Failed, next.Outcomes.Skipped, next.Outcomes.Pending, dialect.Micros(next.Updated), dialect.Micros(next.Completed),
		next.Reference.ID, next.Reference.Incarnation, expected)
	if err != nil {
		return err
	}
	if count != 1 {
		return kernel.ErrConflict
	}
	return nil
}
