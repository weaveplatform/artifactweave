package replication

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RefreshRepository joins a canonical repository update, under the same project
// guard. A pull source follows that repository's upstream URL; changing it
// preserves public target identity and credentials but fences queued workers.
// The caller owns repository-update authorization. No network I/O occurs here.
func (s Service) RefreshRepository(ctx context.Context, key string) error {
	if s.Store == nil || s.Guard == nil || s.Ownership == nil || s.Repositories == nil {
		return fmt.Errorf("replication repository refresh is unavailable: %w", kernel.ErrStore)
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("guard replication source update: %w", err)
		}
		repository, err := s.Ownership.ProjectRepository(ctx, key)
		if err != nil {
			return fmt.Errorf("read replication source ownership: %w", err)
		}
		if repository.Class != repo.Remote {
			return nil
		}
		target, err := s.target(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, ErrMultipleTargets) {
			return nil
		}
		if err != nil {
			return err
		}
		// Legacy advanced configurations keep their P37 owner. They are already
		// refused by this service's public reads and worker admission.
		if target.EventReplication || target.CheckBinaryExistence {
			return nil
		}
		configuration, err := s.Repositories.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read updated replication source: %w", err)
		}
		if target.URL == configuration.URL {
			return nil
		}
		if err := validateURL(configuration.URL); err != nil {
			return err
		}
		target.URL, target.Revision = configuration.URL, rand.Text()
		if err := s.Store.Replications().Put(ctx, target); err != nil {
			return fmt.Errorf("refresh replication source: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replication repository update: %w", err)
	}
	return nil
}
