package sql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

var _ port.RepositoryIndexPages = repos{}

func (r repos) IndexRepository(ctx context.Context, key string) (api.IndexRepository, error) {
	var out api.IndexRepository
	found := false
	err := r.s.Rows(ctx, "index repository context",
		"SELECT key, rclass, package_type, project_key, "+r.projectEnvironmentsSQL()+" FROM repositories WHERE key = ?", []any{key},
		func(row *sql.Rows) error {
			var environments string
			if err := row.Scan(&out.Key, &out.Class, &out.PackageType, &out.Project, &environments); err != nil {
				return err //nolint:wrapcheck // Rows supplies context
			}
			if err := json.Unmarshal([]byte(environments), &out.Environments); err != nil {
				return fmt.Errorf("decode index repository environments: %w", err)
			}
			found = true
			return nil
		})
	if err != nil {
		return api.IndexRepository{}, err
	}
	if !found {
		return api.IndexRepository{}, kernel.ErrNotFound
	}
	return out, nil
}

func (r repos) IndexRepositories(ctx context.Context, query api.IndexRepositoryQuery, page kernel.Page) ([]api.IndexRepository, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("index repositories page: %w", err)
	}
	condition, args, err := dialect.Keyset(page, []string{"key"}, nil, false)
	if err != nil {
		return nil, "", fmt.Errorf("index repositories cursor: %w", err)
	}
	if len(query.PackageTypes) == 0 || len(query.Repositories) == 0 || !query.AllProjects && len(query.Projects) == 0 ||
		query.Environments != nil && len(query.Environments) == 0 {
		return nil, "", nil
	}
	condition += " AND rclass IN ('local', 'remote') AND package_type IN (" + dialect.Placeholders(len(query.PackageTypes)) + ")"
	for _, kind := range query.PackageTypes {
		args = append(args, string(kind))
	}
	if !query.AllProjects {
		condition += " AND project_key IN (" + dialect.Placeholders(len(query.Projects)) + ")"
		for _, project := range query.Projects {
			args = append(args, project)
		}
	}
	include, includeArgs := r.indexRepositoryPatterns(query.Repositories)
	condition += " AND (" + include + ")"
	args = append(args, includeArgs...)
	if len(query.ExcludedRepositories) > 0 {
		exclude, excludeArgs := r.indexRepositoryPatterns(query.ExcludedRepositories)
		condition += " AND NOT (" + exclude + ")"
		args = append(args, excludeArgs...)
	}
	if query.Environments != nil {
		array := "json_each(COALESCE(json_extract(doc, '$.environments'), '[]'))"
		alias := " AS environment"
		if r.s.Dialect().Name == "postgres" {
			array = "jsonb_array_elements_text(COALESCE(NULLIF(doc::jsonb -> 'environments', 'null'::jsonb), '[]'::jsonb))"
			alias += "(value)"
		}
		condition += " AND EXISTS (SELECT 1 FROM " + array + alias + " WHERE environment.value IN (" + dialect.Placeholders(len(query.Environments)) + "))"
		for _, environment := range query.Environments {
			args = append(args, environment)
		}
	}
	var out []api.IndexRepository
	err = r.s.Rows(ctx, "index repositories",
		"SELECT key, rclass, package_type, project_key, "+r.projectEnvironmentsSQL()+" FROM repositories WHERE "+condition+" ORDER BY key LIMIT ?",
		append(args, page.Limit+1), func(row *sql.Rows) error {
			var entry api.IndexRepository
			var environments string
			if err := row.Scan(&entry.Key, &entry.Class, &entry.PackageType, &entry.Project, &environments); err != nil {
				return err //nolint:wrapcheck // Rows supplies context
			}
			if err := json.Unmarshal([]byte(environments), &entry.Environments); err != nil {
				return fmt.Errorf("decode index repository environments: %w", err)
			}
			out = append(out, entry)
			return nil
		})
	if err != nil {
		return nil, "", err
	}
	out, next := kernel.Trim(out, page.Limit, func(entry api.IndexRepository) []string { return []string{entry.Key} })
	return out, next, nil
}

func (r repos) indexRepositoryPatterns(patterns []string) (string, []any) {
	clauses := make([]string, 0, len(patterns))
	args := make([]any, 0, len(patterns))
	for _, pattern := range patterns {
		if r.s.Dialect().Name == "postgres" {
			clauses = append(clauses, "key LIKE ? ESCAPE '!'")
			args = append(args, strings.NewReplacer("!", "!!", "%", "!%", "_", "!_", "*", "%", "?", "_").Replace(pattern))
		} else {
			// SQLite LIKE folds ASCII case; GLOB retains repository identity.
			// Bracket classes are not part of the public wildcard language.
			clauses = append(clauses, "key GLOB ?")
			args = append(args, strings.ReplaceAll(pattern, "[", "[[]"))
		}
	}
	return strings.Join(clauses, " OR "), args
}
