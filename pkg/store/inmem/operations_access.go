package inmem

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Projects implements store.Store.
func (s *Store) Projects() store.ProjectStore { return projectStore{s} }

type projectStore struct{ s *Store }

type (
	roleKey   struct{ project, name string }
	memberKey struct {
		project string
		typ     types.PrincipalType
		name    string
	}
)

func (p projectStore) projects() *table[string, types.Project] {
	return tableOf[string, types.Project](p.s, "projects")
}

func (p projectStore) roles() *table[roleKey, types.Role] {
	return tableOf[roleKey, types.Role](p.s, "roles")
}

func (p projectStore) environments() *table[string, types.Environment] {
	return tableOf[string, types.Environment](p.s, "environments")
}

func (p projectStore) members() *table[memberKey, types.ProjectMember] {
	return tableOf[memberKey, types.ProjectMember](p.s, "project_members")
}

func (p projectStore) assignments() *table[string, types.RepositoryAssignment] {
	return tableOf[string, types.RepositoryAssignment](p.s, "project_repositories")
}

// requireProject returns ErrNotFound unless the project exists; global
// records (an empty key) need none. Call it under the lock.
func (p projectStore) requireProject(key string) error {
	if key != "" && !p.projects().has(key) {
		return notFound("project", key)
	}
	return nil
}

func (p projectStore) Create(ctx context.Context, v types.Project) error {
	return p.s.do(ctx, func() error {
		if p.projects().has(v.Key) {
			return conflict("project", v.Key)
		}
		p.projects().put(v.Key, v)
		return nil
	})
}

func (p projectStore) Get(ctx context.Context, key string) (types.Project, error) {
	var out types.Project
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.projects().get(key); !ok {
			return notFound("project", key)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Update(ctx context.Context, v types.Project) error {
	return p.s.do(ctx, func() error {
		if !p.projects().has(v.Key) {
			return notFound("project", v.Key)
		}
		p.projects().put(v.Key, v)
		return nil
	})
}

func (p projectStore) Delete(ctx context.Context, key string) error {
	return p.s.do(ctx, func() error {
		if !p.projects().del(key) {
			return notFound("project", key)
		}
		p.roles().deleteWhere(func(k roleKey, _ types.Role) bool { return k.project == key })
		p.environments().
			deleteWhere(func(_ string, e types.Environment) bool { return e.ProjectKey == key })
		p.members().
			deleteWhere(func(k memberKey, _ types.ProjectMember) bool { return k.project == key })
		p.assignments().
			deleteWhere(func(_ string, a types.RepositoryAssignment) bool { return a.ProjectKey == key })
		for repo, a := range p.assignments().rows {
			if slices.Contains(a.SharedWith, key) {
				a = store.Clone(a)
				a.SharedWith = slices.DeleteFunc(
					a.SharedWith,
					func(k string) bool { return k == key },
				)
				p.assignments().put(repo, a)
			}
		}
		return nil
	})
}

func (p projectStore) List(ctx context.Context) ([]types.Project, error) {
	var out []types.Project
	err := p.s.do(ctx, func() error {
		out = p.projects().
			all(nil, func(a, b types.Project) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (p projectStore) PutRole(ctx context.Context, r types.Role) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(r.ProjectKey); err != nil {
			return err
		}
		p.roles().put(roleKey{r.ProjectKey, r.Name}, r)
		return nil
	})
}

func (p projectStore) Role(ctx context.Context, projectKey, name string) (types.Role, error) {
	var out types.Role
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.roles().get(roleKey{projectKey, name}); !ok {
			return notFound("role", projectKey+"/"+name)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Roles(ctx context.Context, projectKey string) ([]types.Role, error) {
	var out []types.Role
	err := p.s.do(ctx, func() error {
		out = p.roles().all(func(k roleKey, _ types.Role) bool { return k.project == projectKey },
			func(a, b types.Role) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteRole(ctx context.Context, projectKey, name string) error {
	return p.s.do(ctx, func() error {
		if !p.roles().del(roleKey{projectKey, name}) {
			return notFound("role", projectKey+"/"+name)
		}
		return nil
	})
}

func (p projectStore) CreateEnvironment(ctx context.Context, e types.Environment) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(e.ProjectKey); err != nil {
			return err
		}
		if p.environments().has(e.Name) {
			return conflict("environment", e.Name)
		}
		p.environments().put(e.Name, e)
		return nil
	})
}

func (p projectStore) Environments(
	ctx context.Context,
	projectKey string,
) ([]types.Environment, error) {
	var out []types.Environment
	err := p.s.do(ctx, func() error {
		out = p.environments().
			all(func(_ string, e types.Environment) bool { return e.ProjectKey == projectKey },
				func(a, b types.Environment) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) RenameEnvironment(ctx context.Context, projectKey, from, to string) error {
	return p.s.do(ctx, func() error {
		e, ok := p.environments().get(from)
		if !ok || e.ProjectKey != projectKey {
			return notFound("environment", projectKey+"/"+from)
		}
		if from == to {
			return nil
		}
		if p.environments().has(to) {
			return conflict("environment", to)
		}
		p.environments().del(from)
		e.Name = to
		p.environments().put(to, e)
		for k, r := range p.roles().rows {
			if slices.Contains(r.Environments, from) {
				r = store.Clone(r)
				renameEnv(r.Environments, from, to)
				p.roles().put(k, r)
			}
		}
		for k, a := range p.assignments().rows {
			if slices.Contains(a.Environments, from) {
				a = store.Clone(a)
				renameEnv(a.Environments, from, to)
				p.assignments().put(k, a)
			}
		}
		return nil
	})
}

// renameEnv replaces from with to in names.
func renameEnv(names []string, from, to string) {
	for i, n := range names {
		if n == from {
			names[i] = to
		}
	}
}

func (p projectStore) DeleteEnvironment(ctx context.Context, projectKey, name string) error {
	return p.s.do(ctx, func() error {
		e, ok := p.environments().get(name)
		if !ok || e.ProjectKey != projectKey {
			return notFound("environment", projectKey+"/"+name)
		}
		p.environments().del(name)
		return nil
	})
}

func (p projectStore) PutMember(ctx context.Context, m types.ProjectMember) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(m.ProjectKey); err != nil {
			return err
		}
		p.members().put(memberKey{m.ProjectKey, m.Type, m.Name}, m)
		return nil
	})
}

func (p projectStore) Member(
	ctx context.Context, projectKey string, typ types.PrincipalType, name string,
) (types.ProjectMember, error) {
	var out types.ProjectMember
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.members().get(memberKey{projectKey, typ, name}); !ok {
			return notFound("member", fmt.Sprintf("%s/%s/%s", projectKey, typ, name))
		}
		return nil
	})
	return out, err
}

func (p projectStore) Members(
	ctx context.Context, projectKey string, typ types.PrincipalType,
) ([]types.ProjectMember, error) {
	var out []types.ProjectMember
	err := p.s.do(ctx, func() error {
		out = p.members().all(
			func(k memberKey, _ types.ProjectMember) bool { return k.project == projectKey && k.typ == typ },
			func(a, b types.ProjectMember) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteMember(
	ctx context.Context,
	projectKey string,
	typ types.PrincipalType,
	name string,
) error {
	return p.s.do(ctx, func() error {
		if !p.members().del(memberKey{projectKey, typ, name}) {
			return notFound("member", fmt.Sprintf("%s/%s/%s", projectKey, typ, name))
		}
		return nil
	})
}

func (p projectStore) PutAssignment(ctx context.Context, a types.RepositoryAssignment) error {
	return p.s.do(ctx, func() error {
		if err := p.requireProject(a.ProjectKey); err != nil {
			return err
		}
		a.SharedWith = slices.Compact(slices.Sorted(slices.Values(a.SharedWith)))
		p.assignments().put(a.RepoKey, a)
		return nil
	})
}

func (p projectStore) Assignment(
	ctx context.Context,
	repoKey string,
) (types.RepositoryAssignment, error) {
	var out types.RepositoryAssignment
	err := p.s.do(ctx, func() error {
		var ok bool
		if out, ok = p.assignments().get(repoKey); !ok {
			return notFound("assignment", repoKey)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Assignments(
	ctx context.Context,
	projectKey string,
) ([]types.RepositoryAssignment, error) {
	var out []types.RepositoryAssignment
	err := p.s.do(ctx, func() error {
		out = p.assignments().all(func(_ string, a types.RepositoryAssignment) bool {
			return a.ProjectKey == projectKey || a.SharedWithAll ||
				slices.Contains(a.SharedWith, projectKey)
		}, func(a, b types.RepositoryAssignment) int { return strings.Compare(a.RepoKey, b.RepoKey) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteAssignment(ctx context.Context, repoKey string) error {
	return p.s.do(ctx, func() error {
		if !p.assignments().del(repoKey) {
			return notFound("assignment", repoKey)
		}
		return nil
	})
}
