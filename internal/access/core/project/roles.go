package project

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Domain errors remain distinct from storage failures, which must never become
// missing roles or partial lists. HTTP integration is still pending.
var (
	// ErrInvalid reports an operation outside the project service contract.
	ErrInvalid = errors.New("invalid")
	// ErrNotFound reports a project, role, identity or membership that is absent.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports a role name already available in the project.
	ErrConflict = errors.New("already exists")
	// ErrImmutable refuses changing a role owned by global administration.
	ErrImmutable = errors.New("role cannot be changed through a project")
	// ErrAmbiguousRole refuses an imported or concurrently created name that
	// resolves to both a project role and a global role. The service does not
	// guess a scope or silently grant the actions of the other role.
	ErrAmbiguousRole = errors.New("role name is ambiguous across project and global scopes")
)

// Service manages project roles and memberships over the access unit of work.
// Its caller checks the relevant administration privilege before invoking a
// mutation. It never grants platform or artifact permissions on its own.
// Every compound read and mutation uses one transaction, taking the project
// snapshot guard before inspecting roles, environments or members.
type Service struct {
	// Store provides the access records and their shared transaction runner.
	Store port.Store
	// Repositories joins the access transaction when project lifecycle changes
	// its server-owned build-info repository. Role-only services need no binding.
	Repositories artifactsapi.Repositories
}

func (s Service) tx(ctx context.Context, fn func(context.Context) error) error {
	return s.Store.Tx(ctx, fn) //nolint:wrapcheck // each operation supplies its own context
}

func (s Service) requireProject(ctx context.Context, key string) error {
	_, err := s.Store.Projects().Get(ctx, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("project %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("project: read project %q: %w", key, err)
	}
	return nil
}

// Roles returns the global roles usable in key, with their current global
// overrides, followed by that project's custom roles in stored name order.
// It refuses ambiguous persisted names instead of publishing an unusable list.
func (s Service) Roles(ctx context.Context, key string) ([]model.Role, error) {
	var out []model.Role
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		var err error
		out, err = (roles.Service{Store: s.Store}).Roles(ctx)
		if err != nil {
			return fmt.Errorf("project: read global roles: %w", err)
		}
		custom, err := s.Store.Projects().Roles(ctx, key)
		if err != nil {
			return fmt.Errorf("project: read roles of %q: %w", key, err)
		}
		for _, r := range custom {
			if slices.ContainsFunc(out, func(g model.Role) bool { return g.Name == r.Name }) {
				return fmt.Errorf("project %q role %q: %w", key, r.Name, ErrAmbiguousRole)
			}
		}
		out = append(out, custom...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Role returns the named project custom, global custom or built-in role.
func (s Service) Role(ctx context.Context, key, name string) (model.Role, error) {
	var out model.Role
	err := s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		var err error
		out, err = s.role(ctx, key, name)
		return err
	})
	return out, err
}

func (s Service) role(ctx context.Context, key, name string) (model.Role, error) {
	return ResolveRole(ctx, s.Store.Projects(), key, name)
}

// ResolveRole shares project/global ambiguity and built-in override semantics
// with credential issuance. The caller holds a guarded project snapshot and
// validates project existence before accepting the definition.
func ResolveRole(ctx context.Context, projects port.ProjectStore, key, name string) (model.Role, error) {
	local, localErr := projects.Role(ctx, key, name)
	if localErr != nil && !errors.Is(localErr, kernel.ErrNotFound) {
		return model.Role{}, fmt.Errorf("project: read role %q in %q: %w", name, key, localErr)
	}
	global, globalErr := roles.Lookup(ctx, projects, name)
	if globalErr != nil && !errors.Is(globalErr, roles.ErrNotFound) {
		return model.Role{}, fmt.Errorf("project: read global role %q: %w", name, globalErr)
	}
	switch {
	case localErr == nil && globalErr == nil:
		return model.Role{}, fmt.Errorf("project %q role %q: %w", key, name, ErrAmbiguousRole)
	case localErr == nil:
		return local, nil
	case globalErr == nil:
		return global, nil
	default:
		return model.Role{}, fmt.Errorf("project %q role %q: %w", key, name, ErrNotFound)
	}
}

// CreateRole adds a CUSTOM role after checking every action and environment.
// No membership or authorization changes before all validation succeeds.
func (s Service) CreateRole(ctx context.Context, key string, r model.Role) error {
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		if err := s.checkRole(ctx, key, &r); err != nil {
			return err
		}
		if _, err := s.role(ctx, key, r.Name); err == nil {
			return fmt.Errorf("project %q role %q: %w", key, r.Name, ErrConflict)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return s.putRole(ctx, r)
	})
}

// UpdateRole replaces a project's CUSTOM role. Global and predefined roles
// remain owned by global role administration; a project cannot shadow them.
func (s Service) UpdateRole(ctx context.Context, key, name string, r model.Role) error {
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		current, err := s.role(ctx, key, name)
		if err != nil {
			return err
		}
		if current.ProjectKey != key || current.Type != model.RoleCustom {
			return fmt.Errorf("role %q: %w", name, ErrImmutable)
		}
		if r.Name != name {
			return fmt.Errorf("role name %q differs from %q in the path: %w", r.Name, name, ErrInvalid)
		}
		if err := s.checkRole(ctx, key, &r); err != nil {
			return err
		}
		return s.putRole(ctx, r)
	})
}

// DeleteRole removes a CUSTOM role and its assignments to users and groups
// in key. Members left without a role are removed in the same transaction.
func (s Service) DeleteRole(ctx context.Context, key, name string) error {
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.requireProject(ctx, key); err != nil {
			return err
		}
		r, err := s.role(ctx, key, name)
		if err != nil {
			return err
		}
		if r.ProjectKey != key || r.Type != model.RoleCustom {
			return fmt.Errorf("role %q: %w", name, ErrImmutable)
		}
		if err := s.Store.Projects().DeleteRole(ctx, key, name); err != nil {
			return fmt.Errorf("project: delete role %q in %q: %w", name, key, err)
		}
		return s.unassignRole(ctx, key, name)
	})
}

func (s Service) unassignRole(ctx context.Context, key, name string) error {
	ps := s.Store.Projects()
	for _, typ := range []model.PrincipalType{model.PrincipalUser, model.PrincipalGroup} {
		members, err := ps.Members(ctx, key, typ)
		if err != nil {
			return fmt.Errorf("project: read %s memberships in %q: %w", typ, key, err)
		}
		for _, m := range members {
			if !slices.Contains(m.Roles, name) {
				continue
			}
			m.Roles = slices.DeleteFunc(slices.Clone(m.Roles), func(r string) bool { return r == name })
			if len(m.Roles) == 0 {
				err = ps.DeleteMember(ctx, key, typ, m.Name)
			} else {
				err = ps.PutMember(ctx, m)
			}
			if err != nil {
				return fmt.Errorf("project: remove role %q from %s %q: %w", name, typ, m.Name, err)
			}
		}
	}
	return nil
}

func (s Service) checkRole(ctx context.Context, key string, r *model.Role) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("a role name is required: %w", ErrInvalid)
	}
	if r.Type != model.RoleCustom {
		return fmt.Errorf("a project role must have type CUSTOM: %w", ErrInvalid)
	}
	actions := make([]string, 0, len(r.Actions))
	for _, a := range r.Actions {
		if !model.IsProjectRoleAction(a) {
			return fmt.Errorf("action %q is not a project role action: %w", a, ErrInvalid)
		}
		if !slices.Contains(actions, a) {
			actions = append(actions, a)
		}
	}
	available, err := s.environments(ctx, key)
	if err != nil {
		return err
	}
	envs := make([]string, 0, len(r.Environments))
	for _, e := range r.Environments {
		i := slices.IndexFunc(available, func(name string) bool { return strings.EqualFold(name, e) })
		if i < 0 {
			return fmt.Errorf("environment %q does not exist in project %q: %w", e, key, ErrInvalid)
		}
		if !slices.Contains(envs, available[i]) {
			envs = append(envs, available[i])
		}
	}
	r.ProjectKey, r.Actions, r.Environments = key, actions, envs
	return nil
}

func (s Service) environments(ctx context.Context, key string) ([]string, error) {
	out := slices.Clone(accessapi.BuiltinEnvironments)
	for _, scope := range []string{"", key} {
		stored, err := s.Store.Projects().Environments(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("project: read environments of %q: %w", scope, err)
		}
		for _, e := range stored {
			out = append(out, e.Name)
		}
	}
	return out, nil
}

func (s Service) putRole(ctx context.Context, r model.Role) error {
	if err := s.Store.Projects().PutRole(ctx, r); err != nil {
		return fmt.Errorf("project: write role %q in %q: %w", r.Name, r.ProjectKey, err)
	}
	return nil
}
