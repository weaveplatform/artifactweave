package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

func (p projectStore) Storage(ctx context.Context, key string) (model.ProjectStorage, bool, error) {
	if err := p.guard(ctx); err != nil {
		return model.ProjectStorage{}, false, err
	}
	var used sql.NullInt64
	var warning sql.NullString
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT s.bytes, s.warning_key FROM projects p LEFT JOIN project_storage s ON s.project_key = p.key WHERE p.key = ?"), key).
		Scan(&used, &warning)
	if err != nil {
		return model.ProjectStorage{}, false, p.s.Wrap("read project storage", err)
	}
	return model.ProjectStorage{Bytes: used.Int64, WarningKey: warning.String}, used.Valid, nil
}

func (p projectStore) SetStorage(ctx context.Context, key string, value model.ProjectStorage) error {
	if value.Bytes < 0 {
		return fmt.Errorf("negative project storage: %w", kernel.ErrInvalid)
	}
	return p.tx(ctx, func(ctx context.Context) error {
		if _, err := p.Get(ctx, key); err != nil {
			return err
		}
		_, err := p.s.Exec(
			ctx,
			"write project storage",
			dialect.Upsert("project_storage", []string{"project_key", "bytes", "warning_key"}, []string{"project_key"}),
			key,
			value.Bytes,
			value.WarningKey,
		)
		return err
	})
}
