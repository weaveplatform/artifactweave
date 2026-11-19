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
	environments := "COALESCE(json_extract(doc, '$.environments'), '[]')"
	if r.s.Dialect().Name == "postgres" {
		environments = "COALESCE(doc::jsonb -> 'environments', '[]'::jsonb)::text"
	}
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
