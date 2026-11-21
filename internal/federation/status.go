package federation

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Status supplies read-only history independently of transfer/credential pools.
func (m *Module) Status() (replication.StatusReader, error) {
	store, ok := m.deps.Store.(replication.StatusStore)
	if !ok {
		return replication.StatusReader{}, fmt.Errorf("replication status store unavailable: %w", kernel.ErrStore)
	}
	return replication.StatusReader{Store: store, Authorize: m.deps.currentStatusReader}, nil
}

func (d Deps) currentStatusReader(ctx context.Context, key, root string) (repo.Class, string, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return "", "", fmt.Errorf("replication status caller: %w", err)
	}
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil || d.Ownership == nil || d.Lifetimes == nil {
		return "", "", fmt.Errorf("replication status authority unavailable: %w", kernel.ErrStore)
	}
	if err := d.Access.Projects.Guard(ctx); err != nil {
		return "", "", fmt.Errorf("guard replication status: %w", err)
	}
	p, err = d.Access.Authenticator.Current(ctx, p)
	if errors.Is(err, kernel.ErrNotFound) {
		return "", "", kernel.ErrBadCredentials
	}
	if err != nil {
		return "", "", fmt.Errorf("current replication status credential: %w", err)
	}
	decider, err := d.Access.Authorizer.For(ctx, p)
	if err != nil {
		return "", "", fmt.Errorf("current replication status permissions: %w", err)
	}
	if decider == nil {
		return "", "", kernel.ErrForbidden
	}
	owner, err := d.Ownership.ProjectRepository(ctx, key)
	if err != nil {
		return "", "", fmt.Errorf("replication status repository: %w", err)
	}
	if !decider.AllowsAll(accessapi.ResourceArtifact, accessapi.Repo{Key: owner.Key, Class: string(owner.Class)}, root, accessapi.ActionRead) {
		return "", "", kernel.ErrForbidden
	}
	incarnation, err := d.Lifetimes.RepositoryIncarnation(ctx, owner.Key)
	if err != nil {
		return "", "", fmt.Errorf("replication status repository lifetime: %w", err)
	}
	return owner.Class, incarnation, nil
}
