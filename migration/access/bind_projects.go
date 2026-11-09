package access

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/port"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var _ accessapi.Projects = projects{}

// projects reads projects, their environments and their user members
// (decision 0013). Errors are the store's, unwrapped, so errors.Is(err,
// kernel.ErrNotFound) and each caller's own wrap still apply.
type projects struct{ st port.Store }

func (b projects) Environments(ctx context.Context, project string) ([]string, error) {
	envs, err := b.st.Projects().Environments(ctx, project)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store's errors are the contract's
	}
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}
	return names, nil
}

func (b projects) Member(ctx context.Context, project, user string) error {
	_, err := b.st.Projects().Member(ctx, project, types.PrincipalUser, user)
	return err //nolint:wrapcheck // the store's errors are the contract's
}

func (b projects) Exists(ctx context.Context, project string) error {
	_, err := b.st.Projects().Get(ctx, project)
	return err //nolint:wrapcheck // the store's errors are the contract's
}
