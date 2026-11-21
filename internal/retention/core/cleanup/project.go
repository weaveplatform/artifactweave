package cleanup

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func (s Service) projectIncarnation(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	if s.ProjectIncarnation == nil {
		return "", fmt.Errorf("cleanup project lifetime lookup is unavailable: %w", kernel.ErrStore)
	}
	incarnation, err := s.ProjectIncarnation(ctx, key)
	if err != nil {
		return "", fmt.Errorf("current cleanup project lifetime: %w", err)
	}
	if incarnation == "" {
		return "", fmt.Errorf("cleanup project has no lifetime identity: %w", kernel.ErrStore)
	}
	return incarnation, nil
}

func (s Service) currentProject(ctx context.Context, p model.CleanupPolicy) error {
	actual, err := s.projectIncarnation(ctx, p.ProjectKey)
	if err != nil {
		return err
	}
	if actual != p.ProjectIncarnation {
		return fmt.Errorf("cleanup policy project lifetime changed; verified enablement is required: %w", kernel.ErrConflict)
	}
	return nil
}
