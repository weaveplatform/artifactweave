package accesshttp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// GetProjectRoles lists usable roles for current admins or the project's role
// read scope. Public role types are projected independently of storage types.
func (s AccessService) GetProjectRoles(ctx context.Context, req accessgen.GetProjectRolesRequestObject) (accessgen.GetProjectRolesResponseObject, error) {
	out := accessgen.GetProjectRoles200JSONResponse{}
	err := s.projectAccess(ctx, req.ProjectKey, projectListRoles, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		all, err := svc.Roles(ctx, req.ProjectKey)
		if err != nil {
			return fmt.Errorf("get project roles: %w", err)
		}
		for _, r := range all {
			wire, err := projectRoleWire(r)
			if err != nil {
				return fmt.Errorf("get project roles: %w", err)
			}
			out = append(out, wire)
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "list project roles", err, http.StatusBadRequest)
	}
	return out, nil
}

// GetProjectRole returns one role to a current platform or project admin.
func (s AccessService) GetProjectRole(ctx context.Context, req accessgen.GetProjectRoleRequestObject) (accessgen.GetProjectRoleResponseObject, error) {
	var out accessgen.ProjectRole
	err := s.projectAccess(ctx, req.ProjectKey, projectAdministrator, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		r, err := svc.Role(ctx, req.ProjectKey, req.Role)
		if err != nil {
			return fmt.Errorf("get project role: %w", err)
		}
		out, err = projectRoleWire(r)
		if err != nil {
			return fmt.Errorf("get project role: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "get project role", err, http.StatusBadRequest)
	}
	return accessgen.GetProjectRole200JSONResponse(out), nil
}

// CreateProjectRole validates and creates a custom role, requiring current
// Manage Members delegation for project admins. A name conflict is a 400.
func (s AccessService) CreateProjectRole(ctx context.Context, req accessgen.CreateProjectRoleRequestObject) (accessgen.CreateProjectRoleResponseObject, error) {
	var out accessgen.ProjectRole
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a role body is required")
		}
		b := req.Body
		r := model.Role{
			Name: b.Name, Type: model.RoleType(b.Type), Description: httpkit.Deref(b.Description),
			Actions: httpkit.Deref(b.Actions), Environments: httpkit.Deref(b.Environments),
		}
		if err := svc.CreateRole(ctx, req.ProjectKey, r); err != nil {
			return fmt.Errorf("create project role: %w", err)
		}
		r, err := svc.Role(ctx, req.ProjectKey, b.Name)
		if err != nil {
			return fmt.Errorf("create project role: %w", err)
		}
		out, err = projectRoleWire(r)
		if err != nil {
			return fmt.Errorf("create project role: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "create project role", err, http.StatusBadRequest)
	}
	return accessgen.CreateProjectRole201JSONResponse(out), nil
}

// UpdateProjectRole replaces only a project-owned CUSTOM role. Predefined and
// global roles remain protected by the domain service.
func (s AccessService) UpdateProjectRole(ctx context.Context, req accessgen.UpdateProjectRoleRequestObject) (accessgen.UpdateProjectRoleResponseObject, error) {
	var out accessgen.ProjectRole
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a role body is required")
		}
		b := req.Body
		r := model.Role{
			Name: req.Role, Type: model.RoleType(b.Type), Description: httpkit.Deref(b.Description),
			Actions: httpkit.Deref(b.Actions), Environments: httpkit.Deref(b.Environments),
		}
		if err := svc.UpdateRole(ctx, req.ProjectKey, req.Role, r); err != nil {
			return fmt.Errorf("update project role: %w", err)
		}
		r, err := svc.Role(ctx, req.ProjectKey, req.Role)
		if err != nil {
			return fmt.Errorf("update project role: %w", err)
		}
		out, err = projectRoleWire(r)
		if err != nil {
			return fmt.Errorf("update project role: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "update project role", err, http.StatusBadRequest)
	}
	return accessgen.UpdateProjectRole200JSONResponse(out), nil
}

// DeleteProjectRole removes a custom role and its direct assignments atomically.
func (s AccessService) DeleteProjectRole(ctx context.Context, req accessgen.DeleteProjectRoleRequestObject) (accessgen.DeleteProjectRoleResponseObject, error) {
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		return svc.DeleteRole(ctx, req.ProjectKey, req.Role)
	})
	if err != nil {
		return nil, s.projectError(ctx, "delete project role", err, http.StatusBadRequest)
	}
	return accessgen.DeleteProjectRole204Response{}, nil
}

// GetProjectsForRole returns one page of projects using a global role, after
// checking current platform administration authority in the same transaction.
func (s AccessService) GetProjectsForRole(ctx context.Context, req accessgen.GetProjectsForRoleRequestObject) (accessgen.GetProjectsForRoleResponseObject, error) {
	var out accessgen.GetProjectsForRole200JSONResponse
	err := s.projectAccess(ctx, "", projectPlatform, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		limit := 1000
		if req.Params.Limit != nil {
			limit = *req.Params.Limit
			if limit < 1 {
				return httpkit.BadRequestf("limit must be at least 1")
			}
		}
		projects, next, err := svc.ProjectsForRole(ctx, req.Params.RoleName, httpkit.Deref(req.Params.DescendingOrder),
			kernel.Page{After: httpkit.Deref(req.Params.Cursor), Limit: limit})
		if err != nil {
			return fmt.Errorf("get projects for role: %w", err)
		}
		rows := make([]struct {
			Name       *string `json:"name,omitempty"`
			ProjectKey *string `json:"projectKey,omitempty"` //nolint:tagliatelle // matches the generated Access response
		}, 0, len(projects))
		for _, p := range projects {
			rows = append(rows, struct {
				Name       *string `json:"name,omitempty"`
				ProjectKey *string `json:"projectKey,omitempty"` //nolint:tagliatelle // matches the generated Access response
			}{Name: httpkit.PtrTo(p.DisplayName), ProjectKey: httpkit.PtrTo(p.Key)})
		}
		out.Projects = &rows
		if next != "" {
			out.Cursor = &next
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "list projects for role", err, http.StatusBadRequest)
	}
	return out, nil
}
