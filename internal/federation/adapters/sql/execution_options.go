package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

const purposeExecutionPassword = "replication_execution_options.password" // #nosec G101 -- sealed column purpose

func executionOptionsRowID(keys []string) string {
	// References permit punctuation; a length prefix makes this tuple unique.
	return strconv.Itoa(len(keys[0])) + ":" + keys[0] + keys[1]
}

func (p executions) Options(ctx context.Context, ref model.ExecutionReference) (model.ExecutionOptions, error) {
	if err := ref.Validate(); err != nil {
		return model.ExecutionOptions{}, fmt.Errorf("validate execution options reference: %w", err)
	}
	var document sql.NullString
	var password []byte
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT options_json,password FROM replication_execution_options WHERE run_id=? AND run_incarnation=?"), ref.ID, ref.Incarnation).Scan(&document, &password)
	if err != nil {
		return model.ExecutionOptions{}, p.s.Wrap("read execution options", err)
	}
	var out model.ExecutionOptions
	if !document.Valid || dialect.FromJSON(document, &out) != nil {
		return model.ExecutionOptions{}, fmt.Errorf("stored execution options invalid: %w", kernel.ErrStore)
	}
	out.Password, err = p.s.Unseal(purposeExecutionPassword, executionOptionsRowID([]string{ref.ID, ref.Incarnation}), password)
	if err != nil {
		return model.ExecutionOptions{}, fmt.Errorf("open execution password: %w", err)
	}
	if out.Validate() != nil {
		return model.ExecutionOptions{}, fmt.Errorf("stored execution options invalid: %w", kernel.ErrStore)
	}
	return out, nil
}

func (p executions) PutOptions(ctx context.Context, ref model.ExecutionReference, options model.ExecutionOptions) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("execution options require admission transaction: %w", kernel.ErrInvalid)
	}
	if err := options.Validate(); err != nil {
		return fmt.Errorf("validate execution options: %w", err)
	}
	// Serialize creation and replay using the parent row, including when the
	// option row does not exist yet. Its composite FK also excludes wrong lives.
	run, err := p.Run(ctx, ref)
	if err != nil {
		return fmt.Errorf("lock execution options lifetime: %w", err)
	}
	if !run.Binding.OneShot {
		return kernel.ErrConflict
	}
	options = kernel.Normalise(options)
	current, err := p.Options(ctx, ref)
	if err == nil {
		if current != options {
			return kernel.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	if run.Phase != model.ExecutionPending {
		return kernel.ErrConflict
	}
	document, err := dialect.ToJSON(options)
	if err != nil {
		return fmt.Errorf("encode execution options: %w", err)
	}
	password, err := p.s.Seal(purposeExecutionPassword, executionOptionsRowID([]string{ref.ID, ref.Incarnation}), options.Password)
	if err != nil {
		return fmt.Errorf("seal execution password: %w", err)
	}
	_, err = p.s.Exec(ctx, "create execution options",
		"INSERT INTO replication_execution_options (run_id,run_incarnation,options_json,password) VALUES (?,?,?,?)",
		ref.ID, ref.Incarnation, document, password)
	return err
}
