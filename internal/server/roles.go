package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	"github.com/weaveplatform/artifactweave/internal/service/roles"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Global roles and environments (/access/api/v1/roles, /environments;
// issue #77). Every operation is admin only.

// roleService checks the caller is an administrator and returns the service.
func (s accessService) roleService(ctx context.Context) (roles.Service, error) {
	if err := adminOnly(ctx); err != nil {
		return roles.Service{}, err
	}
	st, err := artifactoryService{cfg: s.cfg}.content()
	if err != nil {
		return roles.Service{}, err
	}
	return roles.Service{Store: st, GlobalLimit: s.cfg.GlobalEnvironmentLimit}, nil
}

// roleError maps the service's errors to statuses. taken is the status of a
// name already in use: 409 on create, 400 on rename (the spec lists no 409
// there).
func (s accessService) roleError(ctx context.Context, op string, err error, taken int) error {
	switch {
	case errors.Is(err, roles.ErrNotFound):
		return apierror.New(http.StatusNotFound, err.Error())
	case errors.Is(err, roles.ErrConflict):
		return apierror.New(taken, err.Error())
	case errors.Is(err, roles.ErrInvalid), errors.Is(err, roles.ErrBuiltin),
		errors.Is(err, roles.ErrInUse):
		return apierror.New(http.StatusBadRequest, err.Error())
	default:
		return artifactoryService{cfg: s.cfg}.internalError(ctx, op, err)
	}
}

func (s accessService) GetGlobalEnvironments(
	ctx context.Context,
	_ accessgen.GetGlobalEnvironmentsRequestObject,
) (accessgen.GetGlobalEnvironmentsResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	names, err := svc.Environments(ctx)
	if err != nil {
		return nil, s.roleError(ctx, "list environments", err, http.StatusConflict)
	}
	out := make(accessgen.GetGlobalEnvironments200JSONResponse, 0, len(names))
	for _, n := range names {
		out = append(out, accessgen.Environment{Name: n})
	}
	return out, nil
}

func (s accessService) CreateGlobalEnvironment(
	ctx context.Context,
	req accessgen.CreateGlobalEnvironmentRequestObject,
) (accessgen.CreateGlobalEnvironmentResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequestf("a body with the environment name is required")
	}
	if err := svc.CreateEnvironment(ctx, req.Body.Name); err != nil {
		return nil, s.roleError(ctx, "create environment", err, http.StatusConflict)
	}
	return accessgen.CreateGlobalEnvironment201JSONResponse{Name: req.Body.Name}, nil
}

func (s accessService) RenameGlobalEnvironment(
	ctx context.Context,
	req accessgen.RenameGlobalEnvironmentRequestObject,
) (accessgen.RenameGlobalEnvironmentResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequestf("a body with new_name is required")
	}
	err = svc.RenameEnvironment(ctx, string(req.EnvironmentName), req.Body.NewName)
	if err != nil {
		return nil, s.roleError(ctx, "rename environment", err, http.StatusBadRequest)
	}
	return accessgen.RenameGlobalEnvironment200JSONResponse{Name: req.Body.NewName}, nil
}

// DeleteGlobalEnvironment refuses DEV, PROD and environments still in use
// with 400, as the operation's description says; its declared responses do
// not list the 400, so the error carries it.
func (s accessService) DeleteGlobalEnvironment(
	ctx context.Context,
	req accessgen.DeleteGlobalEnvironmentRequestObject,
) (accessgen.DeleteGlobalEnvironmentResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.DeleteEnvironment(ctx, string(req.EnvironmentName)); err != nil {
		return nil, s.roleError(ctx, "delete environment", err, http.StatusConflict)
	}
	return accessgen.DeleteGlobalEnvironment204Response{}, nil
}

func (s accessService) GetAllGlobalRoles(
	ctx context.Context,
	_ accessgen.GetAllGlobalRolesRequestObject,
) (accessgen.GetAllGlobalRolesResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	all, err := svc.Roles(ctx)
	if err != nil {
		return nil, s.roleError(ctx, "list roles", err, http.StatusConflict)
	}
	out := make(accessgen.GetAllGlobalRoles200JSONResponse, 0, len(all))
	for _, r := range all {
		out = append(out, globalRoleWire(r))
	}
	return out, nil
}

func (s accessService) GetGlobalRole(
	ctx context.Context,
	req accessgen.GetGlobalRoleRequestObject,
) (accessgen.GetGlobalRoleResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	r, err := svc.Role(ctx, string(req.Role))
	if err != nil {
		return nil, s.roleError(ctx, "get role", err, http.StatusConflict)
	}
	return accessgen.GetGlobalRole200JSONResponse(globalRoleWire(r)), nil
}

// CreateGlobalRole answers 201 with {"code":"OK","message":"Role '<name>'
// created"}, as JFrog's spec documents; the platform provider then reads the
// role back.
func (s accessService) CreateGlobalRole(
	ctx context.Context,
	req accessgen.CreateGlobalRoleRequestObject,
) (accessgen.CreateGlobalRoleResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequestf("a body with the role is required")
	}
	b := req.Body
	r := types.Role{
		Name: b.Name, Type: types.RoleType(b.Type),
		Description: deref(b.Description), Actions: deref(b.Actions),
		Environments: deref(b.Environments),
	}
	if err := svc.CreateRole(ctx, r); err != nil {
		return nil, s.roleError(ctx, "create role", err, http.StatusConflict)
	}
	return accessgen.CreateGlobalRole201JSONResponse{
		Code: "OK", Message: fmt.Sprintf("Role '%s' created", r.Name),
	}, nil
}

func (s accessService) UpdateGlobalRole(
	ctx context.Context,
	req accessgen.UpdateGlobalRoleRequestObject,
) (accessgen.UpdateGlobalRoleResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequestf("a body with the role is required")
	}
	b := req.Body
	r := types.Role{
		Name: b.Name, Type: types.RoleType(b.Type),
		Description: deref(b.Description), Actions: deref(b.Actions),
		Environments: deref(b.Environments),
	}
	if err := svc.UpdateRole(ctx, string(req.Role), r); err != nil {
		return nil, s.roleError(ctx, "update role", err, http.StatusConflict)
	}
	return accessgen.UpdateGlobalRole200Response{}, nil
}

func (s accessService) DeleteGlobalRole(
	ctx context.Context,
	req accessgen.DeleteGlobalRoleRequestObject,
) (accessgen.DeleteGlobalRoleResponseObject, error) {
	svc, err := s.roleService(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.DeleteRole(ctx, string(req.Role)); err != nil {
		return nil, s.roleError(ctx, "delete role", err, http.StatusConflict)
	}
	return accessgen.DeleteGlobalRole204Response{}, nil
}

// globalRoleWire renders a role, with empty lists rather than null.
func globalRoleWire(r types.Role) accessgen.GlobalRole {
	typ := accessgen.GlobalRoleType(r.Type)
	out := accessgen.GlobalRole{
		Name: r.Name, Type: &typ,
		Actions:      append([]string{}, r.Actions...),
		Environments: append([]string{}, r.Environments...),
	}
	if r.Description != "" {
		out.Description = &r.Description
	}
	return out
}

// deref returns *p, or the zero value when p is nil.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
