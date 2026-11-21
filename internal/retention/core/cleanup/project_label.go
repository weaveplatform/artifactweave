package cleanup

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func (s Service) projectDisplayName(ctx context.Context, key string) (string, error) {
	if key == "" || s.ProjectDisplayName == nil {
		return key, nil
	}
	name, err := s.ProjectDisplayName(ctx, key)
	if err != nil {
		return "", fmt.Errorf("read cleanup project display name: %w", err)
	}
	if name == "" {
		name = key
	}
	return name, nil
}

func (s Service) policyProjectDisplayName(ctx context.Context, p model.CleanupPolicy) (string, error) {
	if err := s.currentProject(ctx, p); err != nil {
		// Retained or unbound configuration must not borrow a recreated
		// project's current human name. This fallback grants no authority.
		if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
			return p.ProjectKey, nil
		}
		return "", err
	}
	return s.projectDisplayName(ctx, p.ProjectKey)
}
