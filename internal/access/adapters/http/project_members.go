package accesshttp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// GetProjectUsers lists direct user assignments for delegated admins or the
// project's user-member read scope; group inheritance is not copied into rows.
func (s AccessService) GetProjectUsers(ctx context.Context, req accessgen.GetProjectUsersRequestObject) (accessgen.GetProjectUsersResponseObject, error) {
	out := []accessgen.ProjectUser{}
	err := s.projectAccess(ctx, req.ProjectKey, projectListUsers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		all, err := svc.Members(ctx, req.ProjectKey, model.PrincipalUser)
		if err != nil {
			return fmt.Errorf("get project users: %w", err)
		}
		for _, m := range all {
			out = append(out, projectUserWire(m))
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "list project users", err, http.StatusBadRequest)
	}
	return accessgen.GetProjectUsers200JSONResponse{Members: &out}, nil
}

// GetProjectUser returns a direct assignment after checking current member
// management authority, including the project's Manage Members delegation.
func (s AccessService) GetProjectUser(ctx context.Context, req accessgen.GetProjectUserRequestObject) (accessgen.GetProjectUserResponseObject, error) {
	var out accessgen.ProjectUser
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		m, err := svc.Member(ctx, req.ProjectKey, model.PrincipalUser, req.User)
		if err != nil {
			return fmt.Errorf("get project user: %w", err)
		}
		out = projectUserWire(m)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "get project user", err, http.StatusBadRequest)
	}
	return accessgen.GetProjectUser200JSONResponse(out), nil
}

// AddOrUpdateProjectUser replaces the existing user's direct role assignments.
// The service validates current identity and every role before publication.
func (s AccessService) AddOrUpdateProjectUser(ctx context.Context, req accessgen.AddOrUpdateProjectUserRequestObject) (accessgen.AddOrUpdateProjectUserResponseObject, error) {
	var out accessgen.ProjectUser
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a body with member roles is required")
		}
		m, err := svc.PutMember(ctx, req.ProjectKey, model.PrincipalUser, req.User, httpkit.Deref(req.Body.Roles))
		if err != nil {
			return fmt.Errorf("add or update project user: %w", err)
		}
		out = projectUserWire(m)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "put project user", err, http.StatusBadRequest)
	}
	return accessgen.AddOrUpdateProjectUser200JSONResponse(out), nil
}

// DeleteProjectUser removes the direct assignment without changing group roles.
func (s AccessService) DeleteProjectUser(ctx context.Context, req accessgen.DeleteProjectUserRequestObject) (accessgen.DeleteProjectUserResponseObject, error) {
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		return svc.DeleteMember(ctx, req.ProjectKey, model.PrincipalUser, req.User)
	})
	if err != nil {
		return nil, s.projectError(ctx, "delete project user", err, http.StatusBadRequest)
	}
	return accessgen.DeleteProjectUser204Response{}, nil
}

// GetProjectGroups lists direct group assignments for delegated admins or the
// project's group-member read scope.
func (s AccessService) GetProjectGroups(ctx context.Context, req accessgen.GetProjectGroupsRequestObject) (accessgen.GetProjectGroupsResponseObject, error) {
	out := []accessgen.ProjectGroup{}
	err := s.projectAccess(ctx, req.ProjectKey, projectListGroups, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		all, err := svc.Members(ctx, req.ProjectKey, model.PrincipalGroup)
		if err != nil {
			return fmt.Errorf("get project groups: %w", err)
		}
		for _, m := range all {
			out = append(out, projectGroupWire(m))
		}
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "list project groups", err, http.StatusBadRequest)
	}
	return accessgen.GetProjectGroups200JSONResponse{Members: &out}, nil
}

// GetProjectGroup returns one group's direct project roles to a delegated admin.
func (s AccessService) GetProjectGroup(ctx context.Context, req accessgen.GetProjectGroupRequestObject) (accessgen.GetProjectGroupResponseObject, error) {
	var out accessgen.ProjectGroup
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		m, err := svc.Member(ctx, req.ProjectKey, model.PrincipalGroup, req.Group)
		if err != nil {
			return fmt.Errorf("get project group: %w", err)
		}
		out = projectGroupWire(m)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "get project group", err, http.StatusBadRequest)
	}
	return accessgen.GetProjectGroup200JSONResponse(out), nil
}

// UpdateProjectGroup replaces the existing group's direct role assignments.
func (s AccessService) UpdateProjectGroup(ctx context.Context, req accessgen.UpdateProjectGroupRequestObject) (accessgen.UpdateProjectGroupResponseObject, error) {
	var out accessgen.ProjectGroup
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		if req.Body == nil {
			return httpkit.BadRequestf("a body with member roles is required")
		}
		m, err := svc.PutMember(ctx, req.ProjectKey, model.PrincipalGroup, req.Group, httpkit.Deref(req.Body.Roles))
		if err != nil {
			return fmt.Errorf("update project group: %w", err)
		}
		out = projectGroupWire(m)
		return nil
	})
	if err != nil {
		return nil, s.projectError(ctx, "put project group", err, http.StatusBadRequest)
	}
	return accessgen.UpdateProjectGroup200JSONResponse(out), nil
}

// DeleteProjectGroup removes only this project's direct group assignment.
func (s AccessService) DeleteProjectGroup(ctx context.Context, req accessgen.DeleteProjectGroupRequestObject) (accessgen.DeleteProjectGroupResponseObject, error) {
	err := s.projectAccess(ctx, req.ProjectKey, projectManageMembers, func(ctx context.Context, svc project.Service, _ projectCaller) error {
		return svc.DeleteMember(ctx, req.ProjectKey, model.PrincipalGroup, req.Group)
	})
	if err != nil {
		return nil, s.projectError(ctx, "delete project group", err, http.StatusBadRequest)
	}
	return accessgen.DeleteProjectGroup204Response{}, nil
}
