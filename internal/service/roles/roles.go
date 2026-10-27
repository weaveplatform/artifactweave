// Package roles manages global roles and global environments: the Access
// API's /api/v1/roles and /api/v1/environments (issue #77, decision 0013).
// The built-in roles and environments (types.BuiltinGlobalRoles,
// types.BuiltinEnvironments) are not stored; this package merges them with
// the stored custom ones, and it is how the permission evaluator reads a
// global role (Role, then types.RoleActions).
package roles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Errors the handlers map to statuses. The Access API returns their text
// verbatim, so package prefixes would change existing response messages.
var (
	// ErrInvalid reports a request the API refuses as malformed.
	ErrInvalid = errors.New("invalid")
	// ErrBuiltin reports a change to a built-in role or environment.
	ErrBuiltin = errors.New("built in")
	// ErrNotFound reports a role or environment that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports a name that is already taken.
	ErrConflict = errors.New("already exists")
	// ErrInUse reports an environment that repositories still belong to.
	ErrInUse = errors.New("in use")
)

// DefaultGlobalEnvironmentLimit is how many global environments may exist,
// DEV and PROD among them, when nothing else is configured: Access's
// environments.global-limit (supported Access configurations;
// createglobalenvironment, "By default, Artifactory supports up to 10
// global environments").
const DefaultGlobalEnvironmentLimit = 10

// Service reads and writes global roles and environments in Store.
type Service struct {
	Store store.Store
	// GlobalLimit caps the global environments, DEV and PROD included;
	// zero means DefaultGlobalEnvironmentLimit.
	GlobalLimit int
}

func (s Service) globalLimit() int {
	if s.GlobalLimit <= 0 {
		return DefaultGlobalEnvironmentLimit
	}
	return s.GlobalLimit
}

// Environments returns the global environment names: DEV and PROD, then the
// stored ones by name.
func (s Service) Environments(ctx context.Context) ([]string, error) {
	stored, err := s.Store.Projects().Environments(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("roles: environments: %w", err)
	}
	out := slices.Clone(types.BuiltinEnvironments)
	for _, e := range stored {
		out = append(out, e.Name)
	}
	return out, nil
}

// environment returns the stored name of the global environment called name,
// compared without regard to case.
func (s Service) environment(ctx context.Context, name string) (string, error) {
	all, err := s.Environments(ctx)
	if err != nil {
		return "", err
	}
	for _, e := range all {
		if strings.EqualFold(e, name) {
			return e, nil
		}
	}
	return "", fmt.Errorf("environment %q: %w", name, ErrNotFound)
}

// taken reports whether name clashes with any environment, global or
// project, without regard to case: the store keeps names unique only as
// given.
func (s Service) taken(ctx context.Context, name string) (bool, error) {
	if types.IsBuiltinEnvironment(name) {
		return true, nil
	}
	keys := []string{""}
	projects, err := s.Store.Projects().List(ctx)
	if err != nil {
		return false, fmt.Errorf("roles: projects: %w", err)
	}
	for _, p := range projects {
		keys = append(keys, p.Key)
	}
	for _, k := range keys {
		envs, err := s.Store.Projects().Environments(ctx, k)
		if err != nil {
			return false, fmt.Errorf("roles: environments: %w", err)
		}
		if slices.ContainsFunc(envs, func(e types.Environment) bool {
			return strings.EqualFold(e.Name, name)
		}) {
			return true, nil
		}
	}
	return false, nil
}

// CreateEnvironment adds a global environment, while fewer than the limit
// exist.
func (s Service) CreateEnvironment(ctx context.Context, name string) error {
	if !types.ValidEnvironmentName(name) {
		return fmt.Errorf(
			"environment name %q must be 1 to 32 letters, digits, hyphens or underscores: %w",
			name,
			ErrInvalid,
		)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		taken, err := s.taken(ctx, name)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("environment %q: %w", name, ErrConflict)
		}
		all, err := s.Environments(ctx)
		if err != nil {
			return err
		}
		if limit := s.globalLimit(); len(all) >= limit {
			return fmt.Errorf("at most %d global environments may exist: %w", limit, ErrInvalid)
		}
		err = s.Store.Projects().CreateEnvironment(ctx, types.Environment{Name: name})
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("environment %q: %w", name, ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("roles: create environment: %w", err)
		}
		return nil
	})
}

// RenameEnvironment renames a global environment and every repository and
// role that names it.
func (s Service) RenameEnvironment(ctx context.Context, name, to string) error {
	if types.IsBuiltinEnvironment(name) {
		return fmt.Errorf("environment %q cannot be renamed: %w", name, ErrBuiltin)
	}
	if !types.ValidEnvironmentName(to) {
		return fmt.Errorf(
			"environment name %q must be 1 to 32 letters, digits, hyphens or underscores: %w",
			to,
			ErrInvalid,
		)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		from, err := s.environment(ctx, name)
		if err != nil {
			return err
		}
		// A change of case keeps the same environment; anything else must be free.
		if !strings.EqualFold(from, to) {
			taken, err := s.taken(ctx, to)
			if err != nil {
				return err
			}
			if taken {
				return fmt.Errorf("environment %q: %w", to, ErrConflict)
			}
		}
		// The store renames the environment in roles and project assignments;
		// repository configurations carry their own list.
		if err := s.Store.Projects().RenameEnvironment(ctx, "", from, to); err != nil {
			return fmt.Errorf("roles: rename environment: %w", err)
		}
		return s.renameInRepositories(ctx, from, to)
	})
}

// DeleteEnvironment removes a global environment that no repository or global
// role names.
func (s Service) DeleteEnvironment(ctx context.Context, name string) error {
	if types.IsBuiltinEnvironment(name) {
		return fmt.Errorf("environment %q cannot be deleted: %w", name, ErrBuiltin)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		env, err := s.environment(ctx, name)
		if err != nil {
			return err
		}
		if err := s.inUse(ctx, env); err != nil {
			return err
		}
		// Stored roles are the custom ones and the predefined overrides; the
		// built-in defaults name only DEV and PROD, which cannot be deleted.
		stored, err := s.Store.Projects().Roles(ctx, "")
		if err != nil {
			return fmt.Errorf("roles: roles: %w", err)
		}
		for _, r := range stored {
			if slices.Contains(r.Environments, env) {
				return fmt.Errorf(
					"environment %q is assigned to role %s: %w",
					env,
					r.Name,
					ErrInUse,
				)
			}
		}
		if err := s.Store.Projects().DeleteEnvironment(ctx, "", env); err != nil {
			return fmt.Errorf("roles: delete environment: %w", err)
		}
		return nil
	})
}

// inUse refuses an environment a repository belongs to, through its
// configuration or a project assignment.
func (s Service) inUse(ctx context.Context, env string) error {
	repos, err := s.Store.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return fmt.Errorf("roles: repositories: %w", err)
	}
	for _, c := range repos {
		if slices.Contains(c.Environments, env) {
			return fmt.Errorf(
				"environment %q is assigned to repository %s: %w",
				env,
				c.Key,
				ErrInUse,
			)
		}
	}
	projects, err := s.Store.Projects().List(ctx)
	if err != nil {
		return fmt.Errorf("roles: projects: %w", err)
	}
	for _, p := range projects {
		as, err := s.Store.Projects().Assignments(ctx, p.Key)
		if err != nil {
			return fmt.Errorf("roles: assignments: %w", err)
		}
		for _, a := range as {
			if slices.Contains(a.Environments, env) {
				return fmt.Errorf("environment %q is assigned to repository %s: %w",
					env, a.RepoKey, ErrInUse)
			}
		}
	}
	return nil
}

// renameInRepositories renames from to to in every repository configuration
// that names it.
func (s Service) renameInRepositories(ctx context.Context, from, to string) error {
	repos, err := s.Store.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return fmt.Errorf("roles: repositories: %w", err)
	}
	for _, c := range repos {
		i := slices.Index(c.Environments, from)
		if i < 0 {
			continue
		}
		c.Environments = slices.Clone(c.Environments)
		c.Environments[i] = to
		if err := s.Store.Repos().Update(ctx, c); err != nil {
			return fmt.Errorf("roles: update repository %s: %w", c.Key, err)
		}
	}
	return nil
}

// stored splits the stored global roles into the overrides of predefined
// roles, by name, and the custom roles.
func (s Service) stored(ctx context.Context) (map[string]types.Role, []types.Role, error) {
	rows, err := s.Store.Projects().Roles(ctx, "")
	if err != nil {
		return nil, nil, fmt.Errorf("roles: roles: %w", err)
	}
	overrides := map[string]types.Role{}
	var custom []types.Role
	for _, r := range rows {
		if _, ok := types.BuiltinGlobalRole(r.Name); ok {
			overrides[r.Name] = r
			continue
		}
		custom = append(custom, r)
	}
	return overrides, custom, nil
}

// Roles returns the built-in global roles, with any stored changes to the
// predefined ones, then the custom ones by name.
func (s Service) Roles(ctx context.Context) ([]types.Role, error) {
	overrides, custom, err := s.stored(ctx)
	if err != nil {
		return nil, err
	}
	out := types.BuiltinGlobalRoles()
	for i, b := range out {
		if o, ok := overrides[b.Name]; ok {
			out[i].Actions, out[i].Environments = o.Actions, o.Environments
		}
	}
	return append(out, custom...), nil
}

// Role returns the global role called name: a built-in one with any stored
// change to its actions and environments, or a custom one.
func (s Service) Role(ctx context.Context, name string) (types.Role, error) {
	r, err := s.Store.Projects().Role(ctx, "", name)
	if b, ok := types.BuiltinGlobalRole(name); ok {
		switch {
		case err == nil:
			b.Actions, b.Environments = r.Actions, r.Environments
		case !errors.Is(err, store.ErrNotFound):
			return b, fmt.Errorf("roles: role %s: %w", name, err)
		}
		return b, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return r, fmt.Errorf("role %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return r, fmt.Errorf("roles: role %s: %w", name, err)
	}
	return r, nil
}

// CreateRole adds a CUSTOM_GLOBAL role.
func (s Service) CreateRole(ctx context.Context, r types.Role) error {
	return s.tx(ctx, func(ctx context.Context) error {
		if err := s.check(ctx, &r, types.RoleCustomGlobal); err != nil {
			return err
		}
		if _, ok := types.BuiltinGlobalRole(r.Name); ok {
			return fmt.Errorf("role %q: %w", r.Name, ErrConflict)
		}
		_, custom, err := s.stored(ctx)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(custom, func(c types.Role) bool { return c.Name == r.Name }) {
			return fmt.Errorf("role %q: %w", r.Name, ErrConflict)
		}
		if len(custom) >= types.MaxCustomGlobalRoles {
			return fmt.Errorf("at most %d custom global roles may exist: %w",
				types.MaxCustomGlobalRoles, ErrInvalid)
		}
		return s.put(ctx, r)
	})
}

// UpdateRole replaces the global role called name with r. A custom role
// changes its description, actions and environments; a PREDEFINED one only
// its actions and environments, stored over the built-in defaults. No role
// is renamed or changes type, and the ADMIN role does not change at all.
func (s Service) UpdateRole(ctx context.Context, name string, r types.Role) error {
	if r.Name != name {
		return fmt.Errorf(
			"the role name %q differs from %q in the path: %w",
			r.Name,
			name,
			ErrInvalid,
		)
	}
	b, builtin := types.BuiltinGlobalRole(name)
	if builtin && b.Type == types.RoleAdmin {
		return fmt.Errorf("role %q cannot be changed: %w", name, ErrBuiltin)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		if builtin {
			if err := s.check(ctx, &r, types.RolePredefined); err != nil {
				return err
			}
			r.Description = b.Description
			return s.put(ctx, r)
		}
		if _, err := s.Role(ctx, name); err != nil {
			return err
		}
		if err := s.check(ctx, &r, types.RoleCustomGlobal); err != nil {
			return err
		}
		return s.put(ctx, r)
	})
}

// DeleteRole removes a custom global role and every project membership's
// assignment of it.
func (s Service) DeleteRole(ctx context.Context, name string) error {
	if _, ok := types.BuiltinGlobalRole(name); ok {
		return fmt.Errorf("role %q cannot be deleted: %w", name, ErrBuiltin)
	}
	return s.tx(ctx, func(ctx context.Context) error {
		err := s.Store.Projects().DeleteRole(ctx, "", name)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("role %q: %w", name, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("roles: delete role: %w", err)
		}
		return s.unassign(ctx, name)
	})
}

// unassign removes role from every project membership; a membership left
// with no role is removed.
func (s Service) unassign(ctx context.Context, role string) error {
	projects, err := s.Store.Projects().List(ctx)
	if err != nil {
		return fmt.Errorf("roles: projects: %w", err)
	}
	ps := s.Store.Projects()
	for _, p := range projects {
		for _, typ := range []types.PrincipalType{types.PrincipalUser, types.PrincipalGroup} {
			members, err := ps.Members(ctx, p.Key, typ)
			if err != nil {
				return fmt.Errorf("roles: members of %s: %w", p.Key, err)
			}
			for _, m := range members {
				i := slices.Index(m.Roles, role)
				if i < 0 {
					continue
				}
				m.Roles = slices.Delete(slices.Clone(m.Roles), i, i+1)
				if len(m.Roles) == 0 {
					err = ps.DeleteMember(ctx, m.ProjectKey, m.Type, m.Name)
				} else {
					err = ps.PutMember(ctx, m)
				}
				if err != nil {
					return fmt.Errorf("roles: membership %s/%s: %w", p.Key, m.Name, err)
				}
			}
		}
	}
	return nil
}

// check validates a global role of type typ and canonicalises its
// environment names; actions and environments keep their order without
// duplicates.
func (s Service) check(ctx context.Context, r *types.Role, typ types.RoleType) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("a role name is required: %w", ErrInvalid)
	}
	if r.Type != typ {
		return fmt.Errorf(
			"type %q: the role is %s, and no role changes type: %w",
			r.Type,
			typ,
			ErrInvalid,
		)
	}
	actions := make([]string, 0, len(r.Actions))
	for _, a := range r.Actions {
		if !types.IsGlobalRoleAction(a) {
			return fmt.Errorf("action %q is not a global role action: %w", a, ErrInvalid)
		}
		if !slices.Contains(actions, a) {
			actions = append(actions, a)
		}
	}
	envs := make([]string, 0, len(r.Environments))
	for _, e := range r.Environments {
		name, err := s.environment(ctx, e)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("environment %q does not exist: %w", e, ErrInvalid)
		}
		if err != nil {
			return err
		}
		if !slices.Contains(envs, name) {
			envs = append(envs, name)
		}
	}
	r.Actions, r.Environments, r.ProjectKey = actions, envs, ""
	return nil
}

func (s Service) put(ctx context.Context, r types.Role) error {
	if err := s.Store.Projects().PutRole(ctx, r); err != nil {
		return fmt.Errorf("roles: put role %s: %w", r.Name, err)
	}
	return nil
}

func (s Service) tx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.Store.Tx(ctx, fn) //nolint:wrapcheck // fn's errors are already wrapped
}
