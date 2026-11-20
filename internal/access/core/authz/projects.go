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
	if applied && slices.ContainsFunc(roles, func(role model.Role) bool { return role.Type == model.RoleAdmin }) {
		if d.projectAdmins == nil {
			d.projectAdmins = map[string]bool{}
		}
		d.projectAdmins[key] = true
	}
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
			break
		}
		page.After = next
	}
	return d.sharedProject(ctx, src, key, roles)
}

func (d *Decider) sharedProject(ctx context.Context, src Sources, projectKey string, roles []model.Role) error {
	assignments, err := src.Projects.Assignments(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("authz: project sharing: %w", err)
	}
	for _, a := range assignments {
		// Ownership remains canonical in artifacts. Stored shares grant only to
		// the consuming project's current roles; no producer credential or
		// upstream-discovery mode participates in this calculation.
		if !a.SharedWithAll && !slices.Contains(a.SharedWith, projectKey) {
			continue
		}
		lookup, ok := src.Repositories.(artifactsapi.ProjectRepositoryLookup)
		if !ok {
			return fmt.Errorf("authz: project sharing requires canonical repository lookup: %w", kernel.ErrStore)
		}
		r, err := lookup.ProjectRepository(ctx, a.RepoKey)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("authz: shared repository: %w", err)
		}
		if r.ProjectKey == projectKey || r.Class != artrepo.Local && r.Class != artrepo.Remote {
			continue
		}
		if d.projectLimit != nil {
			d.projectLimit[r.Key] = true
			if a.SharedReadOnly {
				if d.sharedReadOnly == nil {
					d.sharedReadOnly = map[string]bool{}
				}
				d.sharedReadOnly[r.Key] = true
			}
		}
		for _, role := range roles {
			if !roleEnvironment(role.Environments, r.Environments) {
				continue
			}
			actions := model.RoleActions(role.Actions, accessapi.ResourceArtifact)
			if a.SharedReadOnly {
				actions &= accessapi.ActionRead
			}
			if actions != 0 {
				d.rules[accessapi.ResourceArtifact] = append(d.rules[accessapi.ResourceArtifact], rule{target: r.Key, include: compileAll(nil, "**"), actions: actions})
			}
		}
	}
	return nil
}

func (d *Decider) sharingCeiling(key string) accessapi.Actions {
	key, _ = artrepo.ResolveKey(key)
	if d.sharedReadOnly[key] {
		// A project-bound platform credential retains its own content grants,
		// but consuming a shared repository cannot widen that share's ceiling.
		return accessapi.ActionRead
	}
	return allActions
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
	if d.scope != nil && slices.ContainsFunc(d.scope.Entries, func(entry kernel.ScopeEntry) bool {
		return entry.Kind == kernel.ScopeArtifact || entry.Kind == kernel.ScopeBuild ||
			entry.Kind == kernel.ScopeRepo || entry.Kind == kernel.ScopeProject
	}) {
		// Applied roles cannot turn a content/configuration read ceiling
		// into create/delete authority. Management scopes are kept separate.
		return false
	}
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

// ManagesProject uses the same resolved role selection as content and repository
// administration, including explicit named-role tokens. A content or definition
// ceiling cannot borrow the subject's unrestricted membership privileges.
func (d *Decider) ManagesProject(key string, op accessapi.ProjectOperation) bool {
	if d.anonymous || d.managementCeiling() || (op != accessapi.ProjectManage && op != accessapi.ProjectManageMembers) {
		return false
	}
	if d.isAdmin {
		return true
	}
	if key == "" || !d.withinTokenProject(key) || !d.projectAdmins[key] {
		return false
	}
	return op == accessapi.ProjectManage || d.repoAdmins[key].ManageMembers
}

// HasProjectAdministration admits an administrative policy lookup without
// exposing its owner first. Every returned object still needs ManagesProject.
func (d *Decider) HasProjectAdministration() bool {
	if d.ManagesProject("", accessapi.ProjectManage) {
		return true
	}
	for key := range d.projectAdmins {
		if d.ManagesProject(key, accessapi.ProjectManage) {
			return true
		}
	}
	return false
}
