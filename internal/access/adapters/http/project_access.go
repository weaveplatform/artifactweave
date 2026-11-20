package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type projectPrivilege uint8

const (
	projectPlatform projectPrivilege = iota
	projectAdministrator
	projectManageMembers
	projectList
	projectListRoles
	projectListUsers
	projectListGroups
)

type projectCaller struct {
	principal kernel.Principal
	decider   accessapi.Decider
}

// projectAccess joins authorization and the requested operation in one metadata
// transaction. Take the project guard before checking current identity, matching
// identity-deletion and project-role writes. No response escapes before commit.
func (s AccessService) projectAccess(
	ctx context.Context, key string, need projectPrivilege,
	fn func(context.Context, project.Service, projectCaller) error,
) error {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return fmt.Errorf("project caller: %w", err)
	}
	st, err := s.content()
	if err != nil {
		return err
	}
	if s.deps.API.Authenticator == nil || s.deps.API.Authorizer == nil {
		return apierror.ErrNotImplemented
	}
	svc := project.Service{Store: st, Repositories: s.deps.Repositories}
	err = st.Tx(ctx, func(ctx context.Context) error {
		var current model.Project
		if key == "" {
			if err := svc.Guard(ctx); err != nil {
				return fmt.Errorf("guard project administration: %w", err)
			}
		} else {
			var err error
			current, err = svc.Project(ctx, key)
			if err != nil {
				return fmt.Errorf("guard project administration: %w", err)
			}
		}
		currentPrincipal, err := s.deps.API.Authenticator.Current(ctx, p)
		if err != nil {
			return fmt.Errorf("current project caller: %w", err)
		}
		decider, err := s.deps.API.Authorizer.For(ctx, currentPrincipal)
		if err != nil {
			return fmt.Errorf("current project permissions: %w", err)
		}
		caller := projectCaller{principal: currentPrincipal, decider: decider}
		if !decider.IsAdmin() && need != projectList {
			allowed, err := caller.projectAllowed(ctx, svc, current, need)
			if err != nil {
				return err
			}
			if !allowed {
				return kernel.ErrForbidden
			}
		}
		return fn(ctx, svc, caller)
	})
	if err != nil {
		return fmt.Errorf("project administration transaction: %w", err)
	}
	return nil
}

func (p projectCaller) projectAllowed(ctx context.Context, svc project.Service, current model.Project, need projectPrivilege) (bool, error) {
	if need == projectPlatform {
		return false, nil
	}
	if p.projectReadScope(current.Key, need) {
		return true, nil
	}
	user, groups := p.membershipIdentity()
	roles, err := svc.AssignedRoles(ctx, current.Key, user, groups)
	if err != nil {
		return false, fmt.Errorf("current project administrator: %w", err)
	}
	admin := slices.ContainsFunc(roles, func(r model.Role) bool { return r.Type == model.RoleAdmin })
	if need == projectManageMembers || need == projectListUsers || need == projectListGroups {
		admin = admin && current.AdminPrivileges.ManageMembers
	}
	return admin, nil
}

// Membership-based management uses the same applied user/group selection as
// the native evaluator. Resource-only tokens cannot borrow project memberships.
// Named-role token activation remains PRJ-02, together with mint/lifetime checks.
func (p projectCaller) membershipIdentity() (string, []string) {
	subject := accessapi.SubjectOf(p.principal)
	if subject.Scope == nil {
		return subject.Name, p.decider.Groups()
	}
	var user string
	var useGroups bool
	for _, e := range subject.Scope.Entries {
		switch e.Kind {
		case kernel.ScopeUser, kernel.ScopeAdmin:
			user, useGroups = subject.Name, true
		case kernel.ScopeGroups:
			useGroups = true
		}
	}
	if useGroups {
		return user, p.decider.Groups()
	}
	return user, nil
}

func (p projectCaller) projectReadScope(key string, need projectPrivilege) bool {
	area := ""
	switch need {
	case projectListRoles:
		area = "roles"
	case projectListUsers:
		area = "members/users"
	case projectListGroups:
		area = "members/groups"
	default:
		return false
	}
	subject := accessapi.SubjectOf(p.principal)
	if subject.Scope == nil {
		return false
	}
	for _, entry := range subject.Scope.Entries {
		// Project management scopes document members/**. Keep this read check
		// separate from token mint containment and from applied role grants.
		if entry.Kind != kernel.ScopeProject || entry.Project != key || !entry.Actions.Covers(kernel.ScopeRead) {
			continue
		}
		if entry.Target == area || strings.HasPrefix(area, entry.Target+"/") || antpath.CompilePattern(entry.Target).Match(area) {
			return true
		}
	}
	return false
}

func (s AccessService) projectError(ctx context.Context, operation string, err error, conflict int) error {
	var response *apierror.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &response):
		return err
	case errors.Is(err, kernel.ErrBadCredentials), errors.Is(err, kernel.ErrForbidden),
		errors.Is(err, kernel.ErrPasswordExpired), errors.Is(err, kernel.ErrAPIRestricted):
		return err
	case errors.Is(err, project.ErrNotFound):
		return apierror.New(http.StatusNotFound, err.Error())
	case errors.Is(err, project.ErrConflict):
		return apierror.New(conflict, err.Error())
	case errors.Is(err, project.ErrInvalid), errors.Is(err, project.ErrImmutable), errors.Is(err, project.ErrAmbiguousRole):
		return apierror.New(http.StatusBadRequest, err.Error())
	default:
		return s.internalError(ctx, operation, err)
	}
}
