package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artrepo "github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RoleResolver keeps membership-derived grants distinct from an explicitly
// authorized named-role token, including a username-less OIDC identity.
type RoleResolver interface {
	AssignedRoles(context.Context, string, string, []string) ([]model.Role, error)
	NamedRoles(context.Context, string, []string) ([]model.Role, error)
}

func currentProjectToken(ctx context.Context, projects port.ProjectStore, s accessapi.Subject) (bool, error) {
	token := model.Token{ProjectKey: s.ProjectKey, ProjectIncarnations: s.ProjectIncarnations}
	if s.Scope != nil {
		token.Scope = *s.Scope
	}
	if len(token.ProjectKeys()) == 0 {
		return true, nil
	}
	if projects == nil {
		return false, nil
	}
	current, err := project.TokenCurrent(ctx, projects, token)
	if err != nil {
		return false, fmt.Errorf("authz: project token: %w", err)
	}
	return current, nil
}

func (d *Decider) projectRoles(ctx context.Context, src Sources, s accessapi.Subject, sc scopeShape) error {
	if src.Projects == nil {
		return nil
	}
	if src.Roles == nil || src.Repositories == nil {
		return fmt.Errorf("authz: project role sources are incomplete: %w", kernel.ErrStore)
	}
	keys, err := src.Projects.ForPrincipal(ctx, d.user, d.groups)
	if err != nil {
		return fmt.Errorf("authz: principal projects: %w", err)
	}
	selected := namedProjectRoles(s.Scope)
	for key := range selected {
		keys = append(keys, key)
	}
	if s.ProjectKey != "" {
		d.projectLimit = map[string]bool{}
		d.isAdmin = false
		keys = append(keys, s.ProjectKey)
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		if s.ProjectKey != "" && key != s.ProjectKey {
			continue
		}
		if err := d.project(ctx, src, key, selected[key], sc.hasAppliedPermissions); err != nil {
			return err
		}
	}
	return nil
}

func namedProjectRoles(scope *kernel.Scope) map[string][]string {
	out := map[string][]string{}
	if scope != nil {
		for _, e := range scope.Entries {
			if e.Kind == kernel.ScopeRoles {
				out[e.Project] = append(out[e.Project], e.Names...)
			}
		}
	}
	return out
}

func (d *Decider) project(ctx context.Context, src Sources, key string, names []string, applied bool) error {
	p, err := src.Projects.Get(ctx, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("authz: project %q: %w", key, err)
	}
	assigned, err := src.Roles.AssignedRoles(ctx, key, d.user, d.groups)
	if err != nil {
		return fmt.Errorf("authz: assigned project roles: %w", err)
	}
	named, err := src.Roles.NamedRoles(ctx, key, names)
	if err != nil {
		return fmt.Errorf("authz: selected project roles: %w", err)
	}
	roles := slices.Concat(assigned, named)
	if applied && slices.ContainsFunc(roles, managesResources) {
		if d.repoAdmins == nil {
			d.repoAdmins = map[string]model.AdminPrivileges{}
		}
		d.repoAdmins[key] = p.AdminPrivileges
	}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		repos, next, err := src.Repositories.ProjectRepositories(ctx, key, page)
		if err != nil {
			return fmt.Errorf("authz: project repository context: %w", err)
		}
		for _, repo := range repos {
			if d.projectLimit != nil {
				d.projectLimit[repo.Key] = true
			}
			d.addProjectRules(repo, roles)
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

func managesResources(role model.Role) bool {
	return role.Type == model.RoleAdmin || slices.Contains(role.Actions, "MANAGE_RESOURCES")
}

func (d *Decider) addProjectRules(repo artifactsapi.ProjectRepository, roles []model.Role) {
	for _, role := range roles {
		if !roleEnvironment(role.Environments, repo.Environments) {
			continue
		}
		for _, rt := range []accessapi.ResourceType{accessapi.ResourceArtifact, accessapi.ResourceBuild} {
			actions := model.RoleActions(role.Actions, rt)
			if actions != 0 {
				d.rules[rt] = append(d.rules[rt], rule{target: repo.Key, include: compileAll(nil, "**"), actions: actions})
			}
		}
	}
}

func roleEnvironment(role, resource []string) bool {
	if len(resource) == 0 {
		resource = []string{accessapi.EnvironmentDev}
	}
	// Empty role environments have no observed vendor authorization meaning.
	// Fail closed until a role explicitly names an environment; do not borrow
	// the DEV default that ADR0013 assigns to project repositories.
	return slices.ContainsFunc(role, func(allowed string) bool {
		return slices.ContainsFunc(resource, func(actual string) bool { return strings.EqualFold(allowed, actual) })
	})
}

func (d *Decider) inProject(key string) bool {
	if d.projectLimit == nil {
		return true
	}
	key, _ = artrepo.ResolveKey(key)
	return d.projectLimit[key]
}

// ManagesRepository reads effective granular privileges, never legacy fallback
// flags: explicit false class privileges must remain false after normalization.
func (d *Decider) ManagesRepository(projectKey, class string, op accessapi.RepositoryOperation) bool {
	if d.isAdmin {
		return true
	}
	priv, ok := d.repoAdmins[projectKey]
	if !ok || projectKey == "" {
		return false
	}
	switch op {
	case accessapi.RepositoryCreate:
		switch class {
		case "local":
			return priv.CreateLocalRepository
		case "remote":
			return priv.CreateRemoteRepository
		case "virtual":
			return priv.CreateVirtualRepository
		}
	case accessapi.RepositoryDelete:
		switch class {
		case "local":
			return priv.DeleteLocalRepository
		case "remote":
			return priv.DeleteRemoteRepository
		case "virtual":
			return priv.DeleteVirtualRepository
		}
	case accessapi.RepositoryUpdate:
		if class == "remote" {
			return priv.ManageRemoteRepository
		}
		return (class == "local" || class == "virtual") && priv.ManageResources
	}
	return false
}
