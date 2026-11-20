package accesshttp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// GetProjectRepositoryStatus returns one coherent binding and standard-sharing
// snapshot after current platform authority has been admitted.
func (s AccessService) GetProjectRepositoryStatus(
	ctx context.Context,
	req accessgen.GetProjectRepositoryStatusRequestObject,
) (accessgen.GetProjectRepositoryStatusResponseObject, error) {
	var out accessgen.GetProjectRepositoryStatus200JSONResponse
	err := s.repositoryAccess(ctx, "get repository project status", func(ctx context.Context, svc project.Service) error {
		a, err := svc.RepositoryStatus(ctx, req.RepoName)
		if err != nil {
			return fmt.Errorf("get repository project status: %w", err)
		}
		envs, shared := append([]string{}, a.Environments...), append([]string{}, a.SharedWith...)
		out = accessgen.GetProjectRepositoryStatus200JSONResponse{
			ResourceName: &a.RepoKey, Environments: &envs, SharedWithProjects: &shared,
			SharedWithAllProjects: &a.SharedWithAll, SharedReadOnly: &a.SharedReadOnly,
		}
		if a.ProjectKey != "" {
			out.AssignedTo = &a.ProjectKey
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AttachRepositoryToProject publishes the canonical configuration before 204.
func (s AccessService) AttachRepositoryToProject(
	ctx context.Context,
	req accessgen.AttachRepositoryToProjectRequestObject,
) (accessgen.AttachRepositoryToProjectResponseObject, error) {
	if err := s.repositoryAccess(ctx, "attach repository", func(ctx context.Context, svc project.Service) error {
		return svc.AttachRepository(ctx, req.RepoName, req.TargetProjectKey, httpkit.Deref(req.Params.Force))
	}); err != nil {
		return nil, err
	}
	return accessgen.AttachRepositoryToProject204Response{}, nil
}

// UnassignProjectFromRepository keeps repository contents and standard shares.
func (s AccessService) UnassignProjectFromRepository(
	ctx context.Context,
	req accessgen.UnassignProjectFromRepositoryRequestObject,
) (accessgen.UnassignProjectFromRepositoryResponseObject, error) {
	if err := s.repositoryAccess(ctx, "detach repository", func(ctx context.Context, svc project.Service) error {
		return svc.DetachRepository(ctx, req.RepoName)
	}); err != nil {
		return nil, err
	}
	return accessgen.UnassignProjectFromRepository204Response{}, nil
}

// ShareRepositoryWithAllProjects uses the same permission ceiling as explicit
// shares; the query flag never enables producer-side upstream discovery.
func (s AccessService) ShareRepositoryWithAllProjects(
	ctx context.Context,
	req accessgen.ShareRepositoryWithAllProjectsRequestObject,
) (accessgen.ShareRepositoryWithAllProjectsResponseObject, error) {
	if err := s.repositoryAccess(ctx, "share repository with all projects", func(ctx context.Context, svc project.Service) error {
		return svc.ShareRepository(ctx, req.RepoName, "", true, httpkit.Deref(req.Params.ReadOnly))
	}); err != nil {
		return nil, err
	}
	return accessgen.ShareRepositoryWithAllProjects204Response{}, nil
}

// UnshareRepositoryWithAllProjects removes only the all-project share.
func (s AccessService) UnshareRepositoryWithAllProjects(
	ctx context.Context,
	req accessgen.UnshareRepositoryWithAllProjectsRequestObject,
) (accessgen.UnshareRepositoryWithAllProjectsResponseObject, error) {
	if err := s.repositoryAccess(ctx, "unshare repository with all projects", func(ctx context.Context, svc project.Service) error {
		return svc.UnshareRepository(ctx, req.RepoName, "", true)
	}); err != nil {
		return nil, err
	}
	return accessgen.UnshareRepositoryWithAllProjects204Response{}, nil
}

// ShareRepositoryWithTargetProject adds an explicit standard consumer project.
func (s AccessService) ShareRepositoryWithTargetProject(
	ctx context.Context,
	req accessgen.ShareRepositoryWithTargetProjectRequestObject,
) (accessgen.ShareRepositoryWithTargetProjectResponseObject, error) {
	if err := s.repositoryAccess(ctx, "share repository with project", func(ctx context.Context, svc project.Service) error {
		return svc.ShareRepository(ctx, req.RepoName, req.TargetProjectKey, false, httpkit.Deref(req.Params.ReadOnly))
	}); err != nil {
		return nil, err
	}
	return accessgen.ShareRepositoryWithTargetProject204Response{}, nil
}

// UnshareRepositoryWithTargetProject removes only the named consumer project.
func (s AccessService) UnshareRepositoryWithTargetProject(
	ctx context.Context,
	req accessgen.UnshareRepositoryWithTargetProjectRequestObject,
) (accessgen.UnshareRepositoryWithTargetProjectResponseObject, error) {
	if err := s.repositoryAccess(ctx, "unshare repository with project", func(ctx context.Context, svc project.Service) error {
		return svc.UnshareRepository(ctx, req.RepoName, req.TargetProjectKey, false)
	}); err != nil {
		return nil, err
	}
	return accessgen.UnshareRepositoryWithTargetProject204Response{}, nil
}

func (s AccessService) repositoryAccess(ctx context.Context, operation string, fn func(context.Context, project.Service) error) error {
	err := s.projectAccess(ctx, "", projectPlatform, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		return fn(ctx, svc)
	})
	return s.projectError(ctx, operation, err, http.StatusBadRequest)
}
