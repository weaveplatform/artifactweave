package inmem

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (r repos) ProjectRepository(ctx context.Context, key string) (api.ProjectRepository, error) {
	var out api.ProjectRepository
	err := r.s.Do(ctx, func() error {
		entry, ok := r.t().Get(key)
		if !ok {
			return kernel.ErrNotFound
		}
		out = api.ProjectRepository{Key: entry.Key, Class: entry.Class, ProjectKey: entry.ProjectKey, Environments: slices.Clone(entry.Environments)}
		return nil
	})
	if err != nil {
		return api.ProjectRepository{}, fmt.Errorf("repository project context: %w", err)
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
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("project repositories cursor: %w", err)
	}
	var result []api.ProjectRepository
	err = r.s.Do(ctx, func() error {
		for _, entry := range r.t().First(func(_ string, c repo.Config) bool {
			return c.ProjectKey == key && (len(after) == 0 || strings.Compare(c.Key, after[0]) > 0)
		}, compareRepos, page.Limit+1) {
			result = append(result, api.ProjectRepository{Key: entry.Key, Class: entry.Class, ProjectKey: entry.ProjectKey, Environments: slices.Clone(entry.Environments)})
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	result, next := kernel.Trim(result, page.Limit, func(entry api.ProjectRepository) []string { return []string{entry.Key} })
	return result, next, nil
}
