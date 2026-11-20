package token

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (s *Service) storeIssued(ctx context.Context, rec *model.Token) error {
	if len(rec.ProjectKeys()) == 0 {
		return s.st.Tokens().Create(ctx, *rec) //nolint:wrapcheck // Issue wraps store failures
	}
	err := s.st.Tx(ctx, func(ctx context.Context) error {
		if err := s.st.Projects().Guard(ctx); err != nil {
			return fmt.Errorf("token: guard issuance: %w", err)
		}
		if err := project.BindToken(ctx, s.st.Projects(), rec); err != nil {
			if errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
			}
			return fmt.Errorf("token: bind project lifetimes: %w", err)
		}
		return s.st.Tokens().Create(ctx, *rec)
	})
	if err != nil {
		return fmt.Errorf("token: persist project-bound token: %w", err)
	}
	return nil
}

func (s *Service) currentProjects(ctx context.Context, rec model.Token) error {
	current, err := project.TokenCurrent(ctx, s.st.Projects(), rec)
	if err != nil {
		return fmt.Errorf("token: project lifetimes: %w", err)
	}
	if !current {
		return fmt.Errorf("%w: project lifetime ended", authn.ErrInvalidToken)
	}
	return nil
}
