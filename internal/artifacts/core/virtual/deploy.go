package virtual

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrNoDeployTarget is a deploy to a virtual repository that has no local
// deployment repository, which 7.x refuses with 405 (decision 0008, R6).
var ErrNoDeployTarget = errors.New("virtual: no local deployment repository")

// DeployTarget returns the repository a deploy to the virtual repository v
// lands in: its defaultDeploymentRepo, read through rs, when that is a local
// or federated repository.
//
// 7.161.26's UploadServiceUtils.getTargetRepository (248-261) looks the
// defaultDeploymentRepo up among local and federated repositories alone
// (localOrFederatedRepositoryByKey), so one that is unset, that no
// repository has, or that is remote or virtual is ErrNoDeployTarget. A save
// refuses the last three (repos.CheckMembers) and deleting a repository
// clears it from every virtual repository; a deploy still checks, as 7.x
// does, so a configuration that names one anyway sends no file into a
// remote or virtual repository.
//
// The error is rs's for a read that fails.
func DeployTarget(ctx context.Context, rs Repos, v repo.Config) (repo.Config, error) {
	if v.DefaultDeploymentRepo == "" {
		return repo.Config{}, ErrNoDeployTarget
	}
	c, err := rs.Get(ctx, v.DefaultDeploymentRepo)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return repo.Config{}, ErrNoDeployTarget
	case err != nil:
		return repo.Config{}, fmt.Errorf("default deployment repository %s of %s: %w",
			v.DefaultDeploymentRepo, v.Key, err)
	case c.Class != repo.Local && c.Class != repo.Federated:
		return repo.Config{}, ErrNoDeployTarget
	}
	return c, nil
}
