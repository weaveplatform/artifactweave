package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

func (r repos) projectEnvironmentsSQL() string {
	if r.s.Dialect().Name == "postgres" {
		return "COALESCE(doc::jsonb -> 'environments', '[]'::jsonb)::text"
	}
	return "COALESCE(json_extract(doc, '$.environments'), '[]')"
}

func (r repos) ProjectRepository(ctx context.Context, key string) (api.ProjectRepository, error) {
	var out api.ProjectRepository
	found := false
	err := r.s.Rows(ctx, "repository project context",
		"SELECT key, rclass, project_key, "+r.projectEnvironmentsSQL()+" FROM repositories WHERE key = ?", []any{key},
		func(rows *sql.Rows) error {
			var encoded string
			if err := rows.Scan(&out.Key, &out.Class, &out.ProjectKey, &encoded); err != nil {
				return fmt.Errorf("scan repository project context: %w", err)
			}
			if err := json.Unmarshal([]byte(encoded), &out.Environments); err != nil {
				return fmt.Errorf("decode repository environments: %w", err)
			}
			found = true
			return nil
		})
	if err != nil {
		return api.ProjectRepository{}, fmt.Errorf("repository project context: %w", err)
	}
	if !found {
		return api.ProjectRepository{}, kernel.ErrNotFound
	}
	return out, nil
}

func (r repos) ProjectRepositories(ctx context.Context, key string, page kernel.Page) ([]api.ProjectRepository, string, error) {
	if key == "" {
		return nil, "", fmt.Errorf("project repository query requires a project: %w", kernel.ErrInvalid)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("project repositories page: %w", err)
	}
	condition, args, err := dialect.Keyset(page, []string{"key"}, nil, false)
	if err != nil {
		return nil, "", fmt.Errorf("project repositories cursor: %w", err)
	}
	environments := r.projectEnvironmentsSQL()
	var result []api.ProjectRepository
	err = r.s.Rows(
		ctx,
		"project repository contexts",
		"SELECT key, rclass, project_key, "+environments+" FROM repositories WHERE project_key = ? AND "+condition+" ORDER BY key LIMIT ?",
		append(append([]any{key}, args...), page.Limit+1),
		func(rows *sql.Rows) error {
			var entry api.ProjectRepository
			var encoded string
			if err := rows.Scan(&entry.Key, &entry.Class, &entry.ProjectKey, &encoded); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			if err := json.Unmarshal([]byte(encoded), &entry.Environments); err != nil {
				return fmt.Errorf("decode repository environments: %w", err)
			}
			result = append(result, entry)
			return nil
		},
	)
	if err != nil {
		return nil, "", err
	}
	result, next := kernel.Trim(result, page.Limit, func(entry api.ProjectRepository) []string { return []string{entry.Key} })
	return result, next, nil
}
