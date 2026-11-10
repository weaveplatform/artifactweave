package inmem

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Projects returns the project store: projects with their roles,
// environments, members and repository assignments (decision 0013).
func (s *Store) Projects() port.ProjectStore { return projectStore{s} }

type projectStore struct{ s *Store }

type (
	roleKey   struct{ project, name string }
	memberKey struct {
		project string
		typ     model.PrincipalType
		name    string
	}
)

func (p projectStore) projects() *memtx.Table[string, model.Project] {
	return memtx.TableOf[string, model.Project](p.s.Store, "projects")
}

func (p projectStore) roles() *memtx.Table[roleKey, model.Role] {
	return memtx.TableOf[roleKey, model.Role](p.s.Store, "roles")
}

func (p projectStore) environments() *memtx.Table[string, model.Environment] {
	return memtx.TableOf[string, model.Environment](p.s.Store, "environments")
}

func (p projectStore) members() *memtx.Table[memberKey, model.ProjectMember] {
	return memtx.TableOf[memberKey, model.ProjectMember](p.s.Store, "project_members")
}

func (p projectStore) assignments() *memtx.Table[string, model.RepositoryAssignment] {
	return memtx.TableOf[string, model.RepositoryAssignment](p.s.Store, "project_repositories")
}

// requireProject returns ErrNotFound unless the project exists; global
// records (an empty key) need none. Call it under the lock.
func (p projectStore) requireProject(key string) error {
	if key != "" && !p.projects().Has(key) {
		return memtx.NotFound("project", key) //nolint:wrapcheck // wraps kernel.ErrNotFound
	}
	return nil
}

func (p projectStore) Create(ctx context.Context, v model.Project) error {
	return p.s.Do(ctx, func() error {
		if p.projects().Has(v.Key) {
			return memtx.Conflict("project", v.Key)
		}
		p.projects().Put(v.Key, v)
		return nil
	})
}

func (p projectStore) Get(ctx context.Context, key string) (model.Project, error) {
	var out model.Project
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.projects().Get(key); !ok {
			return memtx.NotFound("project", key)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Update(ctx context.Context, v model.Project) error {
	return p.s.Do(ctx, func() error {
		if !p.projects().Has(v.Key) {
			return memtx.NotFound("project", v.Key)
		}
		p.projects().Put(v.Key, v)
		return nil
	})
}

func (p projectStore) Delete(ctx context.Context, key string) error {
	return p.s.Do(ctx, func() error {
		if !p.projects().Del(key) {
			return memtx.NotFound("project", key)
		}
		p.roles().DeleteWhere(func(k roleKey, _ model.Role) bool { return k.project == key })
		p.environments().
			DeleteWhere(func(_ string, e model.Environment) bool { return e.ProjectKey == key })
		p.members().
			DeleteWhere(func(k memberKey, _ model.ProjectMember) bool { return k.project == key })
		p.assignments().
			DeleteWhere(func(_ string, a model.RepositoryAssignment) bool { return a.ProjectKey == key })
		for repo, a := range p.assignments().Rows {
			if slices.Contains(a.SharedWith, key) {
				a = kernel.Clone(a)
				a.SharedWith = slices.DeleteFunc(
					a.SharedWith,
					func(k string) bool { return k == key },
				)
				p.assignments().Put(repo, a)
			}
		}
		return nil
	})
}

func (p projectStore) List(ctx context.Context) ([]model.Project, error) {
	var out []model.Project
	err := p.s.Do(ctx, func() error {
		out = p.projects().
			All(nil, func(a, b model.Project) int { return strings.Compare(a.Key, b.Key) })
		return nil
	})
	return out, err
}

func (p projectStore) PutRole(ctx context.Context, r model.Role) error {
	return p.s.Do(ctx, func() error {
		if err := p.requireProject(r.ProjectKey); err != nil {
			return err
		}
		p.roles().Put(roleKey{r.ProjectKey, r.Name}, r)
		return nil
	})
}

func (p projectStore) Role(ctx context.Context, projectKey, name string) (model.Role, error) {
	var out model.Role
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.roles().Get(roleKey{projectKey, name}); !ok {
			return memtx.NotFound("role", projectKey+"/"+name)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Roles(ctx context.Context, projectKey string) ([]model.Role, error) {
	var out []model.Role
	err := p.s.Do(ctx, func() error {
		out = p.roles().All(func(k roleKey, _ model.Role) bool { return k.project == projectKey },
			func(a, b model.Role) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteRole(ctx context.Context, projectKey, name string) error {
	return p.s.Do(ctx, func() error {
		if !p.roles().Del(roleKey{projectKey, name}) {
			return memtx.NotFound("role", projectKey+"/"+name)
		}
		return nil
	})
}

func (p projectStore) CreateEnvironment(ctx context.Context, e model.Environment) error {
	return p.s.Do(ctx, func() error {
		if err := p.requireProject(e.ProjectKey); err != nil {
			return err
		}
		if p.environments().Has(e.Name) {
			return memtx.Conflict("environment", e.Name)
		}
		p.environments().Put(e.Name, e)
		return nil
	})
}

func (p projectStore) Environments(
	ctx context.Context,
	projectKey string,
) ([]model.Environment, error) {
	var out []model.Environment
	err := p.s.Do(ctx, func() error {
		out = p.environments().
			All(func(_ string, e model.Environment) bool { return e.ProjectKey == projectKey },
				func(a, b model.Environment) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) RenameEnvironment(ctx context.Context, projectKey, from, to string) error {
	return p.s.Do(ctx, func() error {
		e, ok := p.environments().Get(from)
		if !ok || e.ProjectKey != projectKey {
			return memtx.NotFound("environment", projectKey+"/"+from)
		}
		if from == to {
			return nil
		}
		if p.environments().Has(to) {
			return memtx.Conflict("environment", to)
		}
		p.environments().Del(from)
		e.Name = to
		p.environments().Put(to, e)
		for k, r := range p.roles().Rows {
			if slices.Contains(r.Environments, from) {
				r = kernel.Clone(r)
				renameEnv(r.Environments, from, to)
				p.roles().Put(k, r)
			}
		}
		for k, a := range p.assignments().Rows {
			if slices.Contains(a.Environments, from) {
				a = kernel.Clone(a)
				renameEnv(a.Environments, from, to)
				p.assignments().Put(k, a)
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
	return p.s.Do(ctx, func() error {
		e, ok := p.environments().Get(name)
		if !ok || e.ProjectKey != projectKey {
			return memtx.NotFound("environment", projectKey+"/"+name)
		}
		p.environments().Del(name)
		return nil
	})
}

func (p projectStore) PutMember(ctx context.Context, m model.ProjectMember) error {
	return p.s.Do(ctx, func() error {
		if err := p.requireProject(m.ProjectKey); err != nil {
			return err
		}
		p.members().Put(memberKey{m.ProjectKey, m.Type, m.Name}, m)
		return nil
	})
}

func (p projectStore) Member(
	ctx context.Context, projectKey string, typ model.PrincipalType, name string,
) (model.ProjectMember, error) {
	var out model.ProjectMember
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.members().Get(memberKey{projectKey, typ, name}); !ok {
			return memtx.NotFound("member", fmt.Sprintf("%s/%s/%s", projectKey, typ, name))
		}
		return nil
	})
	return out, err
}

func (p projectStore) Members(
	ctx context.Context, projectKey string, typ model.PrincipalType,
) ([]model.ProjectMember, error) {
	var out []model.ProjectMember
	err := p.s.Do(ctx, func() error {
		out = p.members().All(
			func(k memberKey, _ model.ProjectMember) bool { return k.project == projectKey && k.typ == typ },
			func(a, b model.ProjectMember) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteMember(
	ctx context.Context,
	projectKey string,
	typ model.PrincipalType,
	name string,
) error {
	return p.s.Do(ctx, func() error {
		if !p.members().Del(memberKey{projectKey, typ, name}) {
			return memtx.NotFound("member", fmt.Sprintf("%s/%s/%s", projectKey, typ, name))
		}
		return nil
	})
}

func (p projectStore) PutAssignment(ctx context.Context, a model.RepositoryAssignment) error {
	return p.s.Do(ctx, func() error {
		if err := p.requireProject(a.ProjectKey); err != nil {
			return err
		}
		a.SharedWith = slices.Compact(slices.Sorted(slices.Values(a.SharedWith)))
		p.assignments().Put(a.RepoKey, a)
		return nil
	})
}

func (p projectStore) Assignment(
	ctx context.Context,
	repoKey string,
) (model.RepositoryAssignment, error) {
	var out model.RepositoryAssignment
	err := p.s.Do(ctx, func() error {
		var ok bool
		if out, ok = p.assignments().Get(repoKey); !ok {
			return memtx.NotFound("assignment", repoKey)
		}
		return nil
	})
	return out, err
}

func (p projectStore) Assignments(
	ctx context.Context,
	projectKey string,
) ([]model.RepositoryAssignment, error) {
	var out []model.RepositoryAssignment
	err := p.s.Do(ctx, func() error {
		out = p.assignments().All(func(_ string, a model.RepositoryAssignment) bool {
			return a.ProjectKey == projectKey || a.SharedWithAll ||
				slices.Contains(a.SharedWith, projectKey)
		}, func(a, b model.RepositoryAssignment) int { return strings.Compare(a.RepoKey, b.RepoKey) })
		return nil
	})
	return out, err
}

func (p projectStore) DeleteAssignment(ctx context.Context, repoKey string) error {
	return p.s.Do(ctx, func() error {
		if !p.assignments().Del(repoKey) {
			return memtx.NotFound("assignment", repoKey)
		}
		return nil
	})
}
