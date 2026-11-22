package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

func (p executions) SelectedPaths(ctx context.Context, ref model.ExecutionReference, path string) (int64, bool, error) {
	if _, err := p.Run(ctx, ref); err != nil {
		return 0, false, err
	}
	var count, present int64
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT COUNT(DISTINCT path),COALESCE(MAX(CASE WHEN path=? THEN 1 ELSE 0 END),0) FROM replication_execution_effects WHERE run_id=? AND run_incarnation=?"),
		path, ref.ID, ref.Incarnation).Scan(&count, &present)
	return count, present != 0, p.s.Wrap("count selected replication paths", err)
}

const executionEffectColumns = "run_id, run_incarnation, operation_key, transfer_id, transfer_incarnation, intent_fingerprint, " +
	"transfer_version, path, outcome, reason, effect_paths_json, updated_at"

func (p executions) effect(ctx context.Context, run model.ExecutionRun, key string) (model.ExecutionEffect, error) {
	var out model.ExecutionEffect
	var paths string
	var updated sql.NullInt64
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind("SELECT "+executionEffectColumns+
		" FROM replication_execution_effects WHERE run_id=? AND run_incarnation=? AND operation_key=?"+p.s.LockClause(ctx)),
		run.Reference.ID, run.Reference.Incarnation, key).Scan(
		&out.Run.ID, &out.Run.Incarnation, &out.OperationKey, &out.TransferID, &out.TransferIncarnation, &out.Fingerprint,
		&out.Version, &out.Path, &out.Outcome, &out.Reason, &paths, &updated)
	if err != nil {
		return model.ExecutionEffect{}, p.s.Wrap("read replication effect", err)
	}
	out.Updated = dialect.FromMicros(updated)
	if dialect.FromJSON(sql.NullString{String: paths, Valid: true}, &out.Paths) != nil || out.Validate(run) != nil {
		return model.ExecutionEffect{}, fmt.Errorf("stored replication effect invalid: %w", kernel.ErrStore)
	}
	return out, nil
}

func (p executions) Effect(ctx context.Context, ref model.ExecutionReference, key string) (model.ExecutionEffect, error) {
	var out model.ExecutionEffect
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, ref)
		if err != nil {
			return err
		}
		out, err = p.effect(ctx, run, key)
		return err
	})
	if err != nil {
		return model.ExecutionEffect{}, fmt.Errorf("read selected replication effect: %w", err)
	}
	return out, nil
}

func (p executions) RecordEffect(ctx context.Context, input model.ExecutionEffect) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("replication effect requires a transaction: %w", kernel.ErrInvalid)
	}
	input = kernel.Normalise(input)
	run, err := p.Run(ctx, input.Run)
	if err != nil {
		return err
	}
	var previous *model.ExecutionEffect
	old, err := p.effect(ctx, run, input.OperationKey)
	if err == nil {
		previous = &old
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	next, changed, err := model.ApplyExecutionEffect(run, previous, input)
	if err != nil {
		return fmt.Errorf("record replication effect: %w", err)
	}
	if !changed {
		return nil
	}
	// ToJSON maps a nil slice to SQL NULL. This column stores JSON, including
	// literal null for an unexecuted intent with no proven affected paths.
	paths, err := json.Marshal(input.Paths)
	if err != nil {
		return fmt.Errorf("encode replication effect paths: %w", err)
	}
	if _, err := p.s.Exec(ctx, "write replication effect",
		dialect.Upsert("replication_execution_effects", dialect.OpsCols(executionEffectColumns), []string{"run_id", "run_incarnation", "operation_key"}),
		input.Run.ID, input.Run.Incarnation, input.OperationKey, input.TransferID, input.TransferIncarnation, input.Fingerprint,
		input.Version, input.Path, input.Outcome, input.Reason, string(paths), dialect.Micros(input.Updated)); err != nil {
		return err
	}
	if err := p.indexEffectPaths(ctx, input); err != nil {
		return err
	}
	return p.Advance(ctx, run.Version, next)
}

func (p executions) indexEffectPaths(ctx context.Context, input model.ExecutionEffect) error {
	if _, err := p.s.Exec(ctx, "replace replication effect path index",
		"DELETE FROM replication_execution_effect_paths WHERE run_id=? AND run_incarnation=? AND operation_key=?",
		input.Run.ID, input.Run.Incarnation, input.OperationKey); err != nil {
		return err
	}
	paths := slices.Clone(input.Paths)
	if !slices.Contains(paths, input.Path) {
		paths = append(paths, input.Path)
	}
	for _, path := range paths {
		if _, err := p.s.Exec(ctx, "index replication effect path",
			"INSERT INTO replication_execution_effect_paths (run_id,run_incarnation,operation_key,path) VALUES (?,?,?,?)",
			input.Run.ID, input.Run.Incarnation, input.OperationKey, path); err != nil {
			return err
		}
	}
	return nil
}

func effectPathPredicate(path string) (string, []any) {
	if path == "" {
		return "1=1", nil
	}
	// The indexed byte-collated range has literal path semantics, including
	// uppercase, Unicode, '%' and '_'. A LIKE prefix is case-folded on SQLite.
	return "(scope.path=? OR (scope.path>? AND scope.path<?))", []any{path, path + "/", path + "0"}
}

func matchingEffects(path string) (string, []any) {
	predicate, args := effectPathPredicate(path)
	return "EXISTS (SELECT 1 FROM replication_execution_effects selected WHERE selected.run_id=e.run_id AND selected.run_incarnation=e.incarnation AND " +
		"EXISTS (SELECT 1 FROM replication_execution_effect_paths scope WHERE scope.run_id=selected.run_id AND " +
		"scope.run_incarnation=selected.run_incarnation AND scope.operation_key=selected.operation_key AND " + predicate + "))", args
}

func (p executions) PathStatus(ctx context.Context, target, incarnation, path string) (model.ExecutionPathState, error) {
	where := "e.target_key=? AND e.repo_incarnation=?"
	args := []any{target, incarnation}
	if path != "" {
		matched, values := matchingEffects(path)
		where += " AND " + matched
		args = append(args, values...)
	}
	from := " FROM replication_executions e LEFT JOIN replications target ON target.replication_key=e.target_key " +
		"LEFT JOIN replication_execution_options options ON options.run_id=e.run_id AND options.run_incarnation=e.incarnation " +
		"WHERE (target.replication_key IS NOT NULL OR options.independent=TRUE) AND " + where
	var ref model.ExecutionReference
	var phase model.ExecutionPhase
	var started, completed sql.NullInt64
	counts := model.ExecutionOutcomes{}
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT e.run_id,e.incarnation,e.phase,e.started_at,e.completed_at,e.succeeded,e.failed,e.skipped,e.pending"+from+
			" ORDER BY e.started_at DESC,e.run_id DESC LIMIT 1"), args...).Scan(
		&ref.ID, &ref.Incarnation, &phase, &started, &completed, &counts.Succeeded, &counts.Failed, &counts.Skipped, &counts.Pending)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ExecutionPathState{}, nil
	}
	if err != nil {
		return model.ExecutionPathState{}, p.s.Wrap("read replication path execution", err)
	}
	if path != "" {
		counts, err = p.pathCounts(ctx, ref, path)
		if err != nil {
			return model.ExecutionPathState{}, err
		}
	}
	var lastCompleted sql.NullInt64
	if err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind("SELECT MAX(e.completed_at)"+from), args...).Scan(&lastCompleted); err != nil {
		return model.ExecutionPathState{}, p.s.Wrap("read last completed replication path", err)
	}
	return model.ExecutionPathState{
		Latest:        model.ReplicationRun{ReplicationKey: target, Started: dialect.FromMicros(started), Completed: dialect.FromMicros(completed), Status: counts.Status(phase)},
		LastCompleted: dialect.FromMicros(lastCompleted),
	}, nil
}

func (p executions) pathCounts(ctx context.Context, ref model.ExecutionReference, path string) (model.ExecutionOutcomes, error) {
	predicate, args := effectPathPredicate(path)
	query := `SELECT COALESCE(SUM(CASE WHEN selected.outcome IN ('succeeded','retained') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN selected.outcome='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN selected.outcome='skipped' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN selected.outcome='pending' THEN 1 ELSE 0 END),0)
		FROM replication_execution_effects selected WHERE selected.run_id=? AND selected.run_incarnation=? AND
		EXISTS (SELECT 1 FROM replication_execution_effect_paths scope WHERE scope.run_id=selected.run_id AND
		scope.run_incarnation=selected.run_incarnation AND scope.operation_key=selected.operation_key AND ` + predicate + ")"
	args = append([]any{ref.ID, ref.Incarnation}, args...)
	var counts model.ExecutionOutcomes
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(query), args...).Scan(&counts.Succeeded, &counts.Failed, &counts.Skipped, &counts.Pending)
	return counts, p.s.Wrap("count selected replication path effects", err)
}
