package accesshttp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// GetProjectsList returns every project to a platform admin and only currently
// administered projects to a project admin.
func (s AccessService) GetProjectsList(ctx context.Context, _ accessgen.GetProjectsListRequestObject) (accessgen.GetProjectsListResponseObject, error) {
	out := accessgen.GetProjectsList200JSONResponse{}
	err := s.projectAccess(ctx, "", projectList, func(ctx context.Context, svc project.Service, caller projectCaller) error {
		all, err := svc.Projects(ctx)
		if err != nil {
			return fmt.Errorf("get projects list: %w", err)
		}
		for _, p := range all {
			if !caller.projectAllowed(p.Key, projectAdministrator) {
				continue
			}
			out = append(out, projectWire(p))
		}
		if !caller.decider.IsAdmin() && len(out) == 0 {
			return kernel.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "list projects", err, http.StatusBadRequest)
	}
	return out, nil
}

// CreateProject creates the project and its server-owned build-info repository
// atomically, for a current platform administrator.
func (s AccessService) CreateProject(ctx context.Context, req accessgen.CreateProjectRequestObject) (accessgen.CreateProjectResponseObject, error) {
	var out accessgen.Project
	err := s.projectAccess(ctx, "", projectPlatform, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a project body is required")
		}
		p := projectFromCreate(*req.Body)
		if err := svc.CreateProject(ctx, p); err != nil {
			return fmt.Errorf("create project: %w", err)
		}
		out = projectWire(p)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "create project", err, http.StatusConflict)
	}
	return accessgen.CreateProject201JSONResponse(out), nil
}

// GetProject returns current settings to a platform admin or that project's
// current administrator, without exposing storage-only fields.
func (s AccessService) GetProject(ctx context.Context, req accessgen.GetProjectRequestObject) (accessgen.GetProjectResponseObject, error) {
	var out accessgen.Project
	err := s.projectAccess(ctx, req.ProjectKey, projectAdministrator, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		p, err := svc.Project(ctx, req.ProjectKey)
		if err != nil {
			return fmt.Errorf("get project: %w", err)
		}
		out = projectWire(p)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "get project", err, http.StatusBadRequest)
	}
	return accessgen.GetProject200JSONResponse(out), nil
}

// UpdateProject preserves omitted fields while replacing explicitly supplied
// settings. Only a current platform administrator may change delegation/quota.
func (s AccessService) UpdateProject(ctx context.Context, req accessgen.UpdateProjectRequestObject) (accessgen.UpdateProjectResponseObject, error) {
	var out accessgen.Project
	err := s.projectAccess(ctx, req.ProjectKey, projectPlatform, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a project body is required")
		}
		p, err := svc.Project(ctx, req.ProjectKey)
		if err != nil {
			return fmt.Errorf("update project: %w", err)
		}
		p = projectFromUpdate(p, *req.Body)
		if err := svc.UpdateProject(ctx, req.ProjectKey, p); err != nil {
			return fmt.Errorf("update project: %w", err)
		}
		out = projectWire(p)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "update project", err, http.StatusBadRequest)
	}
	return accessgen.UpdateProject200JSONResponse(out), nil
}

// DeleteProject removes project administration and unassigns its repositories,
// retaining repository keys and contents.
func (s AccessService) DeleteProject(ctx context.Context, req accessgen.DeleteProjectRequestObject) (accessgen.DeleteProjectResponseObject, error) {
	err := s.projectAccess(ctx, req.ProjectKey, projectPlatform, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		return svc.DeleteProject(ctx, req.ProjectKey)
	})
	if err != nil {
		return nil, s.projectError(ctx, "delete project", err, http.StatusBadRequest)
	}
	return accessgen.DeleteProject204Response{}, nil
}

// GetProjectEnvironments lists only the environments defined in this project.
func (s AccessService) GetProjectEnvironments(ctx context.Context, req accessgen.GetProjectEnvironmentsRequestObject) (accessgen.GetProjectEnvironmentsResponseObject, error) {
	out := accessgen.GetProjectEnvironments200JSONResponse{}
	err := s.projectAccess(ctx, string(req.ProjectKey), projectAdministrator, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		all, err := svc.Environments(ctx, string(req.ProjectKey))
		if err != nil {
			return fmt.Errorf("get project environments: %w", err)
		}
		for _, e := range all {
			out = append(out, accessgen.Environment{Name: e.Name})
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "list project environments", err, http.StatusBadRequest)
	}
	return out, nil
}

// CreateProjectEnvironment creates a project-prefixed environment for a current
// platform or project administrator, subject to the documented project limit.
func (s AccessService) CreateProjectEnvironment(
	ctx context.Context,
	req accessgen.CreateProjectEnvironmentRequestObject,
) (accessgen.CreateProjectEnvironmentResponseObject, error) {
	var name string
	err := s.projectAccess(ctx, string(req.ProjectKey), projectAdministrator, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a body with the environment name is required")
		}
		e, err := svc.CreateEnvironment(ctx, string(req.ProjectKey), req.Body.Name)
		if err != nil {
			return fmt.Errorf("create project environment: %w", err)
		}
		name = e.Name
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "create project environment", err, http.StatusConflict)
	}
	return accessgen.CreateProjectEnvironment201JSONResponse{Name: name}, nil
}

// RenameProjectEnvironment changes its spelling and all references atomically.
func (s AccessService) RenameProjectEnvironment(
	ctx context.Context,
	req accessgen.RenameProjectEnvironmentRequestObject,
) (accessgen.RenameProjectEnvironmentResponseObject, error) {
	var name string
	err := s.projectAccess(ctx, string(req.ProjectKey), projectAdministrator, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a body with new_name is required")
		}
		e, err := svc.RenameEnvironment(ctx, string(req.ProjectKey), string(req.EnvironmentName), req.Body.NewName)
		if err != nil {
			return fmt.Errorf("rename project environment: %w", err)
		}
		name = e.Name
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "rename project environment", err, http.StatusBadRequest)
	}
	return accessgen.RenameProjectEnvironment200JSONResponse{Name: name}, nil
}

// DeleteProjectEnvironment refuses environments still referenced by project
// roles, repository configurations or assignments.
func (s AccessService) DeleteProjectEnvironment(
	ctx context.Context,
	req accessgen.DeleteProjectEnvironmentRequestObject,
) (accessgen.DeleteProjectEnvironmentResponseObject, error) {
	err := s.projectAccess(ctx, string(req.ProjectKey), projectAdministrator, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		return svc.DeleteEnvironment(ctx, string(req.ProjectKey), string(req.EnvironmentName))
	})
	if err != nil {
		return nil, s.projectError(ctx, "delete project environment", err, http.StatusBadRequest)
	}
	return accessgen.DeleteProjectEnvironment204Response{}, nil
}
