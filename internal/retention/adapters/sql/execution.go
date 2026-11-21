package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func (c cleanupStore) CreateExecution(ctx context.Context, e model.CleanupExecution) error {
	e = kernel.Normalise(e)
	e.Policy.ExecutionCredential = kernel.CredentialReference{}
	if err := e.Validate(); err != nil {
		return err //nolint:wrapcheck // the model names the invalid execution
	}
	snapshot, err := dialect.ToJSON(e)
	if err != nil {
		return err
	}
	_, err = c.s.Exec(ctx, "create cleanup execution",
		"INSERT INTO cleanup_executions (run_id, occurrence, snapshot) VALUES (?, ?, ?)",
		e.RunID, dialect.NullString(e.Occurrence), snapshot)
	return err
}

func scanCleanupExecution(row interface{ Scan(...any) error }) (model.CleanupExecution, error) {
	var e model.CleanupExecution
	var occurrence, snapshot sql.NullString
	if err := row.Scan(&e.RunID, &occurrence, &snapshot); err != nil {
		return e, err //nolint:wrapcheck // the caller supplies the store operation
	}
	e.Occurrence = occurrence.String
	if err := dialect.FromJSON(snapshot, &e); err != nil {
		return e, err
	}
	return e, nil
}

func (c cleanupStore) Execution(ctx context.Context, id int64) (model.CleanupExecution, error) {
	e, err := scanCleanupExecution(c.s.Q(ctx).QueryRowContext(ctx, c.s.Dialect().Rebind(
		"SELECT run_id, occurrence, snapshot FROM cleanup_executions WHERE run_id = ?"+c.s.LockClause(ctx)), id))
	return e, c.s.Wrap("get cleanup execution", err)
}

func (c cleanupStore) ExecutionForOccurrence(ctx context.Context, occurrence string) (model.CleanupExecution, error) {
	if occurrence == "" {
		return model.CleanupExecution{}, fmt.Errorf("a cleanup occurrence is required: %w", kernel.ErrInvalid)
	}
	e, err := scanCleanupExecution(c.s.Q(ctx).QueryRowContext(ctx, c.s.Dialect().Rebind(
		"SELECT run_id, occurrence, snapshot FROM cleanup_executions WHERE occurrence = ?"+c.s.LockClause(ctx)), occurrence))
	return e, c.s.Wrap("get cleanup occurrence", err)
}
