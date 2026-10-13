package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Projects implements store.Store.
func (s *Store) Projects() store.ProjectStore { return projectStore{s} }

// projectStore keeps projects, roles, environments, project_members,
// project_repositories and project_repository_shares. Roles of every scope
// share one table keyed by (project_key, name) with ” for global roles,
// which is research 15's roles and project_roles in one.
type projectStore struct{ s *Store }

// opsCols splits a column list written as "a, b, c".
func opsCols(list string) []string { return strings.Split(list, ", ") }

const projectCols = "key, display_name, description, storage_quota_bytes, soft_limit, quota_email, admin_privileges"

func projectArgs(v types.Project) ([]any, error) {
	priv, err := toJSON(v.AdminPrivileges)
	if err != nil {
		return nil, err
	}
	return []any{
		v.Key, v.DisplayName, v.Description, v.StorageQuotaBytes, v.SoftLimit, v.QuotaEmail, priv,
	}, nil
}

func scanProject(row interface{ Scan(...any) error }) (types.Project, error) {
	var (
		v    types.Project
		priv sql.NullString
	)
	if err := row.Scan(&v.Key, &v.DisplayName, &v.Description, &v.StorageQuotaBytes,
		&v.SoftLimit, &v.QuotaEmail, &priv); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	return v, fromJSON(priv, &v.AdminPrivileges)
}

func (p projectStore) Create(ctx context.Context, v types.Project) error {
	args, err := projectArgs(v)
	if err != nil {
		return err
	}
	_, err = p.s.exec(
		ctx,
		"create project "+v.Key,
		Insert("projects", opsCols(projectCols)),
		args...)
	return err
}

func (p projectStore) Get(ctx context.Context, key string) (types.Project, error) {
	v, err := scanProject(p.s.q(ctx).QueryRowContext(ctx,
		p.s.d.Rebind("SELECT "+projectCols+" FROM projects WHERE key = ?"), key))
	return v, p.s.wrap("get project "+key, err)
}

func (p projectStore) Update(ctx context.Context, v types.Project) error {
	args, err := projectArgs(v)
	if err != nil {
		return err
	}
	return p.s.execOne(
		ctx,
		"update project "+v.Key,
		"UPDATE projects SET display_name = ?, description = ?, "+
			"storage_quota_bytes = ?, soft_limit = ?, quota_email = ?, admin_privileges = ? WHERE key = ?",
		append(args[1:], v.Key)...)
}

func (p projectStore) Delete(ctx context.Context, key string) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.s.execOne(
			ctx,
			"delete project "+key,
			"DELETE FROM projects WHERE key = ?",
			key,
		); err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM roles WHERE project_key = ?",
			"DELETE FROM environments WHERE project_key = ?",
			"DELETE FROM project_members WHERE project_key = ?",
			"DELETE FROM project_repository_shares WHERE project_key = ?",
			"DELETE FROM project_repository_shares WHERE repo_key IN " +
				"(SELECT repo_key FROM project_repositories WHERE project_key = ?)",
			"DELETE FROM project_repositories WHERE project_key = ?",
		} {
			if _, err := p.s.exec(ctx, "delete project "+key, q, key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p projectStore) List(ctx context.Context) ([]types.Project, error) {
	var out []types.Project
	err := p.s.rows(ctx, "list projects", "SELECT "+projectCols+" FROM projects ORDER BY key", nil,
		func(r *sql.Rows) error {
			v, err := scanProject(r)
			out = append(out, v)
			return err
		})
	return out, err
}

// requireProject returns ErrNotFound unless the project exists; global
// records (an empty key) need none.
func (p projectStore) requireProject(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	ok, err := p.s.exists(ctx, "find project "+key, "SELECT 1 FROM projects WHERE key = ?", key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("project %s: %w", key, store.ErrNotFound)
	}
	return nil
}

const roleCols = "project_key, name, type, description, environments, actions"

func scanRole(row interface{ Scan(...any) error }) (types.Role, error) {
	var (
		v          types.Role
		envs, acts sql.NullString
	)
	if err := row.Scan(&v.ProjectKey, &v.Name, &v.Type, &v.Description, &envs, &acts); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	if err := fromJSON(envs, &v.Environments); err != nil {
		return v, err
	}
	return v, fromJSON(acts, &v.Actions)
}

func (p projectStore) PutRole(ctx context.Context, r types.Role) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, r.ProjectKey); err != nil {
			return err
		}
		envs, err := toJSON(r.Environments)
		if err != nil {
			return err
		}
		acts, err := toJSON(r.Actions)
		if err != nil {
			return err
		}
		_, err = p.s.exec(
			ctx,
			"put role "+r.Name,
			Upsert("roles", opsCols(roleCols), []string{"project_key", "name"}),
			r.ProjectKey,
			r.Name,
			r.Type,
			r.Description,
			envs,
			acts,
		)
		return err
	})
}

func (p projectStore) Role(ctx context.Context, projectKey, name string) (types.Role, error) {
	v, err := scanRole(p.s.q(ctx).QueryRowContext(ctx,
		p.s.d.Rebind("SELECT "+roleCols+" FROM roles WHERE project_key = ? AND name = ?"),
		projectKey, name),
	)
	return v, p.s.wrap(fmt.Sprintf("get role %s/%s", projectKey, name), err)
}

func (p projectStore) Roles(ctx context.Context, projectKey string) ([]types.Role, error) {
	return p.roles(
		ctx,
		"SELECT "+roleCols+" FROM roles WHERE project_key = ? ORDER BY name",
		projectKey,
	)
}

func (p projectStore) roles(ctx context.Context, q string, args ...any) ([]types.Role, error) {
	var out []types.Role
	err := p.s.rows(ctx, "list roles", q, args, func(r *sql.Rows) error {
		v, err := scanRole(r)
		out = append(out, v)
		return err
	})
	return out, err
}

func (p projectStore) DeleteRole(ctx context.Context, projectKey, name string) error {
	return p.s.execOne(ctx, fmt.Sprintf("delete role %s/%s", projectKey, name),
		"DELETE FROM roles WHERE project_key = ? AND name = ?", projectKey, name)
}

func (p projectStore) CreateEnvironment(ctx context.Context, e types.Environment) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, e.ProjectKey); err != nil {
			return err
		}
		_, err := p.s.exec(ctx, "create environment "+e.Name,
			Insert("environments", []string{"name", "project_key"}), e.Name, e.ProjectKey)
		return err
	})
}

func (p projectStore) Environments(
	ctx context.Context,
	projectKey string,
) ([]types.Environment, error) {
	var out []types.Environment
	err := p.s.rows(
		ctx,
		"list environments",
		"SELECT name, project_key FROM environments WHERE project_key = ? ORDER BY name",
		[]any{projectKey},
		func(r *sql.Rows) error {
			var v types.Environment
			err := r.Scan(&v.Name, &v.ProjectKey)
			out = append(out, v)
			return err //nolint:wrapcheck // wrapped by rows
		},
	)
	return out, err
}

func (p projectStore) RenameEnvironment(ctx context.Context, projectKey, from, to string) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.s.execOne(
			ctx,
			fmt.Sprintf("rename environment %s/%s", projectKey, from),
			"UPDATE environments SET name = ? WHERE name = ? AND project_key = ?",
			to,
			from,
			projectKey,
		); err != nil {
			return err
		}
		roles, err := p.roles(ctx, "SELECT "+roleCols+" FROM roles WHERE environments IS NOT NULL")
		if err != nil {
			return err
		}
		for _, r := range roles {
			if renameIn(r.Environments, from, to) {
				envs, err := toJSON(r.Environments)
				if err != nil {
					return err
				}
				if _, err := p.s.exec(ctx, "rename environment in role "+r.Name,
					"UPDATE roles SET environments = ? WHERE project_key = ? AND name = ?",
					envs, r.ProjectKey, r.Name); err != nil {
					return err
				}
			}
		}
		assigned, err := p.assignments(ctx, "WHERE environments IS NOT NULL")
		if err != nil {
			return err
		}
		for _, a := range assigned {
			if renameIn(a.Environments, from, to) {
				envs, err := toJSON(a.Environments)
				if err != nil {
					return err
				}
				if _, err := p.s.exec(
					ctx,
					"rename environment in assignment "+a.RepoKey,
					"UPDATE project_repositories SET environments = ? WHERE repo_key = ?",
					envs,
					a.RepoKey,
				); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// renameIn replaces from with to in names and reports whether it did.
func renameIn(names []string, from, to string) bool {
	changed := false
	for i, n := range names {
		if n == from {
			names[i] = to
			changed = true
		}
	}
	return changed
}

func (p projectStore) DeleteEnvironment(ctx context.Context, projectKey, name string) error {
	return p.s.execOne(ctx, fmt.Sprintf("delete environment %s/%s", projectKey, name),
		"DELETE FROM environments WHERE name = ? AND project_key = ?", name, projectKey)
}

const memberCols = "project_key, principal_type, principal_name, roles"

func scanMember(row interface{ Scan(...any) error }) (types.ProjectMember, error) {
	var (
		v     types.ProjectMember
		roles sql.NullString
	)
	if err := row.Scan(&v.ProjectKey, &v.Type, &v.Name, &roles); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	return v, fromJSON(roles, &v.Roles)
}

func (p projectStore) PutMember(ctx context.Context, m types.ProjectMember) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, m.ProjectKey); err != nil {
			return err
		}
		roles, err := toJSON(m.Roles)
		if err != nil {
			return err
		}
		_, err = p.s.exec(
			ctx,
			"put member "+m.Name,
			Upsert(
				"project_members",
				opsCols(memberCols),
				[]string{"project_key", "principal_type", "principal_name"},
			),
			m.ProjectKey,
			m.Type,
			m.Name,
			roles,
		)
		return err
	})
}

func (p projectStore) Member(
	ctx context.Context, projectKey string, typ types.PrincipalType, name string,
) (types.ProjectMember, error) {
	v, err := scanMember(p.s.q(ctx).QueryRowContext(ctx, p.s.d.Rebind("SELECT "+memberCols+
		" FROM project_members WHERE project_key = ? AND principal_type = ? AND principal_name = ?"),
		projectKey, typ, name))
	return v, p.s.wrap(fmt.Sprintf("get member %s/%s/%s", projectKey, typ, name), err)
}

func (p projectStore) Members(
	ctx context.Context, projectKey string, typ types.PrincipalType,
) ([]types.ProjectMember, error) {
	var out []types.ProjectMember
	err := p.s.rows(ctx, "list members", "SELECT "+memberCols+
		" FROM project_members WHERE project_key = ? AND principal_type = ? ORDER BY principal_name",
		[]any{projectKey, typ}, func(r *sql.Rows) error {
			v, err := scanMember(r)
			out = append(out, v)
			return err
		})
	return out, err
}

func (p projectStore) DeleteMember(
	ctx context.Context,
	projectKey string,
	typ types.PrincipalType,
	name string,
) error {
	return p.s.execOne(
		ctx,
		fmt.Sprintf("delete member %s/%s/%s", projectKey, typ, name),
		"DELETE FROM project_members WHERE project_key = ? AND principal_type = ? AND principal_name = ?",
		projectKey,
		typ,
		name,
	)
}

const assignmentCols = "repo_key, project_key, environments, shared_with_all, shared_read_only, updated_at"

func (p projectStore) PutAssignment(ctx context.Context, a types.RepositoryAssignment) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, a.ProjectKey); err != nil {
			return err
		}
		envs, err := toJSON(a.Environments)
		if err != nil {
			return err
		}
		if _, err := p.s.exec(
			ctx,
			"put assignment "+a.RepoKey,
			Upsert("project_repositories", opsCols(assignmentCols), []string{"repo_key"}),
			a.RepoKey,
			a.ProjectKey,
			envs,
			a.SharedWithAll,
			a.SharedReadOnly,
			micros(a.Updated),
		); err != nil {
			return err
		}
		if _, err := p.s.exec(ctx, "put assignment "+a.RepoKey,
			"DELETE FROM project_repository_shares WHERE repo_key = ?", a.RepoKey); err != nil {
			return err
		}
		for _, k := range uniqueShares(a.SharedWith) {
			if _, err := p.s.exec(
				ctx,
				"share "+a.RepoKey,
				Insert(
					"project_repository_shares",
					[]string{"repo_key", "project_key"},
				),
				a.RepoKey,
				k,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// uniqueShares returns the share list sorted and without duplicates.
func uniqueShares(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// assignments reads the assignments matching where (a WHERE clause over
// project_repositories, with "?" placeholders), ordered by repository,
// with their shares.
func (p projectStore) assignments(
	ctx context.Context,
	where string,
	args ...any,
) ([]types.RepositoryAssignment, error) {
	var out []types.RepositoryAssignment
	err := p.s.rows(ctx, "list assignments",
		"SELECT "+assignmentCols+" FROM project_repositories "+where+" ORDER BY repo_key", args,
		func(r *sql.Rows) error {
			var (
				v       types.RepositoryAssignment
				envs    sql.NullString
				updated sql.NullInt64
			)
			if err := r.Scan(
				&v.RepoKey,
				&v.ProjectKey,
				&envs,
				&v.SharedWithAll,
				&v.SharedReadOnly,
				&updated,
			); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			v.Updated = fromMicros(updated)
			err := fromJSON(envs, &v.Environments)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, err
	}
	// Shares are read after the listing is closed: an in-memory SQLite
	// database has one connection, which the open listing would hold.
	for i := range out {
		err := p.s.rows(
			ctx,
			"list shares",
			"SELECT project_key FROM project_repository_shares WHERE repo_key = ? ORDER BY project_key",
			[]any{out[i].RepoKey},
			func(r *sql.Rows) error {
				var k string
				err := r.Scan(&k)
				out[i].SharedWith = append(out[i].SharedWith, k)
				return err //nolint:wrapcheck // wrapped by rows
			},
		)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p projectStore) Assignment(
	ctx context.Context,
	repoKey string,
) (types.RepositoryAssignment, error) {
	got, err := p.assignments(ctx, "WHERE repo_key = ?", repoKey)
	if err != nil {
		return types.RepositoryAssignment{}, err
	}
	if len(got) == 0 {
		return types.RepositoryAssignment{}, fmt.Errorf(
			"get assignment %s: %w",
			repoKey,
			store.ErrNotFound,
		)
	}
	return got[0], nil
}

func (p projectStore) Assignments(
	ctx context.Context,
	projectKey string,
) ([]types.RepositoryAssignment, error) {
	return p.assignments(ctx, "WHERE project_key = ? OR shared_with_all = ? OR repo_key IN "+
		"(SELECT repo_key FROM project_repository_shares WHERE project_key = ?)", projectKey, true, projectKey)
}

func (p projectStore) DeleteAssignment(ctx context.Context, repoKey string) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.exec(ctx, "delete assignment "+repoKey,
			"DELETE FROM project_repository_shares WHERE repo_key = ?", repoKey); err != nil {
			return err
		}
		return p.s.execOne(ctx, "delete assignment "+repoKey,
			"DELETE FROM project_repositories WHERE repo_key = ?", repoKey)
	})
}
