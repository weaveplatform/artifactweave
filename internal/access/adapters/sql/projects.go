package sql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Projects returns the project store: projects with their roles,
// environments, members and repository assignments (decision 0013).
func (s *Store) Projects() port.ProjectStore { return projectStore{s} }

// projectStore keeps projects, roles, environments, project_members,
// project_repositories and project_repository_shares. Roles of every scope
// share one table keyed by (project_key, name) with ” for global roles,
// which is research 15's roles and project_roles in one.
type projectStore struct{ s *Store }

func (p projectStore) Guard(ctx context.Context) error { return p.guard(ctx) }

const projectCols = "key, display_name, description, storage_quota_bytes, soft_limit, quota_email, admin_privileges"

func projectArgs(v model.Project) ([]any, error) {
	priv, err := dialect.ToJSON(v.AdminPrivileges)
	if err != nil {
		return nil, err
	}
	return []any{
		v.Key, v.DisplayName, v.Description, v.StorageQuotaBytes, v.SoftLimit, v.QuotaEmail, priv,
	}, nil
}

func scanProject(row interface{ Scan(...any) error }) (model.Project, error) {
	var (
		v    model.Project
		priv sql.NullString
	)
	if err := row.Scan(&v.Key, &v.DisplayName, &v.Description, &v.StorageQuotaBytes,
		&v.SoftLimit, &v.QuotaEmail, &priv); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	return v, dialect.FromJSON(priv, &v.AdminPrivileges)
}

func (p projectStore) Create(ctx context.Context, v model.Project) error {
	args, err := projectArgs(v)
	if err != nil {
		return err
	}
	a := port.NewProjectAuthority()
	return p.tx(ctx, func(ctx context.Context) error {
		_, err := p.s.Exec(
			ctx,
			"create project "+v.Key,
			dialect.Insert("projects", append(dialect.OpsCols(projectCols), "incarnation", "policy_revision")),
			append(args, a.Incarnation, a.Revision)...)
		return err
	})
}

func (p projectStore) Get(ctx context.Context, key string) (model.Project, error) {
	if err := p.guard(ctx); err != nil {
		return model.Project{}, err
	}
	v, err := scanProject(p.s.Q(ctx).QueryRowContext(ctx,
		p.s.Dialect().Rebind("SELECT "+projectCols+" FROM projects WHERE key = ?"+p.s.LockClause(ctx)), key))
	return v, p.s.Wrap("get project "+key, err)
}

func (p projectStore) Update(ctx context.Context, v model.Project) error {
	args, err := projectArgs(v)
	if err != nil {
		return err
	}
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.s.ExecOne(
			ctx,
			"update project "+v.Key,
			"UPDATE projects SET display_name = ?, description = ?, "+
				"storage_quota_bytes = ?, soft_limit = ?, quota_email = ?, admin_privileges = ?, policy_revision = ? WHERE key = ?",
			append(args[1:], port.NewProjectAuthority().Revision, v.Key)...); err != nil {
			return err
		}
		return p.s.storageChanged(ctx, v.Key)
	})
}

func (p projectStore) Delete(ctx context.Context, key string) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.s.ExecOne(
			ctx,
			"delete project "+key,
			"DELETE FROM projects WHERE key = ?",
			key,
		); err != nil {
			return err
		}
		// Removing this receiving project changes every surviving explicit
		// share that names it, even though the producer assignment remains.
		if _, err := p.s.Exec(ctx, "revoke deleted project's shares",
			"UPDATE project_repositories SET sharing_revision = ? WHERE repo_key IN "+
				"(SELECT repo_key FROM project_repository_shares WHERE project_key = ?)",
			port.NewSharingAuthority().Revision, key); err != nil {
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
			if _, err := p.s.Exec(ctx, "delete project "+key, q, key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p projectStore) List(ctx context.Context) ([]model.Project, error) {
	if err := p.guard(ctx); err != nil {
		return nil, err
	}
	var out []model.Project
	err := p.s.Rows(ctx, "list projects", "SELECT "+projectCols+" FROM projects ORDER BY key", nil,
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
	if err := p.guard(ctx); err != nil {
		return err
	}
	if key == "" {
		return nil
	}
	ok, err := p.s.Exists(ctx, "find project "+key, "SELECT 1 FROM projects WHERE key = ?"+p.s.LockClause(ctx), key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("project %s: %w", key, kernel.ErrNotFound)
	}
	return nil
}

const roleCols = "project_key, name, type, description, environments, actions"

func scanRole(row interface{ Scan(...any) error }) (model.Role, error) {
	var (
		v          model.Role
		envs, acts sql.NullString
	)
	if err := row.Scan(&v.ProjectKey, &v.Name, &v.Type, &v.Description, &envs, &acts); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	if err := dialect.FromJSON(envs, &v.Environments); err != nil {
		return v, err
	}
	return v, dialect.FromJSON(acts, &v.Actions)
}

func (p projectStore) PutRole(ctx context.Context, r model.Role) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, r.ProjectKey); err != nil {
			return err
		}
		envs, err := dialect.ToJSON(r.Environments)
		if err != nil {
			return err
		}
		acts, err := dialect.ToJSON(r.Actions)
		if err != nil {
			return err
		}
		_, err = p.s.Exec(
			ctx,
			"put role "+r.Name,
			dialect.Upsert("roles", dialect.OpsCols(roleCols), []string{"project_key", "name"}),
			r.ProjectKey,
			r.Name,
			r.Type,
			r.Description,
			envs,
			acts,
		)
		if err != nil {
			return err
		}
		return p.rotatePolicy(ctx, r.ProjectKey)
	})
}

func (p projectStore) Role(ctx context.Context, projectKey, name string) (model.Role, error) {
	if err := p.guard(ctx); err != nil {
		return model.Role{}, err
	}
	v, err := scanRole(p.s.Q(ctx).QueryRowContext(ctx,
		p.s.Dialect().Rebind("SELECT "+roleCols+" FROM roles WHERE project_key = ? AND name = ?"),
		projectKey, name),
	)
	return v, p.s.Wrap(fmt.Sprintf("get role %s/%s", projectKey, name), err)
}

func (p projectStore) Roles(ctx context.Context, projectKey string) ([]model.Role, error) {
	return p.roles(
		ctx,
		"SELECT "+roleCols+" FROM roles WHERE project_key = ? ORDER BY name",
		projectKey,
	)
}

func (p projectStore) roles(ctx context.Context, q string, args ...any) ([]model.Role, error) {
	if err := p.guard(ctx); err != nil {
		return nil, err
	}
	var out []model.Role
	err := p.s.Rows(ctx, "list roles", q, args, func(r *sql.Rows) error {
		v, err := scanRole(r)
		out = append(out, v)
		return err
	})
	return out, err
}

func (p projectStore) DeleteRole(ctx context.Context, projectKey, name string) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.s.ExecOne(ctx, fmt.Sprintf("delete role %s/%s", projectKey, name),
			"DELETE FROM roles WHERE project_key = ? AND name = ?", projectKey, name); err != nil {
			return err
		}
		return p.rotatePolicy(ctx, projectKey)
	})
}

func (p projectStore) CreateEnvironment(ctx context.Context, e model.Environment) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, e.ProjectKey); err != nil {
			return err
		}
		_, err := p.s.Exec(ctx, "create environment "+e.Name,
			dialect.Insert("environments", []string{"name", "project_key"}), e.Name, e.ProjectKey)
		if err != nil {
			return err
		}
		return p.rotatePolicy(ctx, e.ProjectKey)
	})
}

func (p projectStore) Environments(
	ctx context.Context,
	projectKey string,
) ([]model.Environment, error) {
	if err := p.guard(ctx); err != nil {
		return nil, err
	}
	var out []model.Environment
	err := p.s.Rows(
		ctx,
		"list environments",
		"SELECT name, project_key FROM environments WHERE project_key = ? ORDER BY name",
		[]any{projectKey},
		func(r *sql.Rows) error {
			var v model.Environment
			err := r.Scan(&v.Name, &v.ProjectKey)
			out = append(out, v)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

func (p projectStore) RenameEnvironment(ctx context.Context, projectKey, from, to string) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.s.ExecOne(
			ctx,
			fmt.Sprintf("rename environment %s/%s", projectKey, from),
			"UPDATE environments SET name = ? WHERE name = ? AND project_key = ?",
			to,
			from,
			projectKey,
		); err != nil {
			return err
		}
		if from == to {
			return nil
		}
		roles, err := p.roles(ctx, "SELECT "+roleCols+" FROM roles WHERE environments IS NOT NULL")
		if err != nil {
			return err
		}
		for _, r := range roles {
			if renameIn(r.Environments, from, to) {
				envs, err := dialect.ToJSON(r.Environments)
				if err != nil {
					return err
				}
				if _, err := p.s.Exec(ctx, "rename environment in role "+r.Name,
					"UPDATE roles SET environments = ? WHERE project_key = ? AND name = ?",
					envs, r.ProjectKey, r.Name); err != nil {
					return err
				}
				if err := p.rotatePolicy(ctx, r.ProjectKey); err != nil {
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
				envs, err := dialect.ToJSON(a.Environments)
				if err != nil {
					return err
				}
				if _, err := p.s.Exec(
					ctx,
					"rename environment in assignment "+a.RepoKey,
					"UPDATE project_repositories SET environments = ?, sharing_revision = ? WHERE repo_key = ?",
					envs,
					port.NewSharingAuthority().Revision,
					a.RepoKey,
				); err != nil {
					return err
				}
			}
		}
		return p.rotatePolicy(ctx, projectKey)
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
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.s.ExecOne(ctx, fmt.Sprintf("delete environment %s/%s", projectKey, name),
			"DELETE FROM environments WHERE name = ? AND project_key = ?", name, projectKey); err != nil {
			return err
		}
		return p.rotatePolicy(ctx, projectKey)
	})
}

const memberCols = "project_key, principal_type, principal_name, roles"

func scanMember(row interface{ Scan(...any) error }) (model.ProjectMember, error) {
	var (
		v     model.ProjectMember
		roles sql.NullString
	)
	if err := row.Scan(&v.ProjectKey, &v.Type, &v.Name, &roles); err != nil {
		return v, err //nolint:wrapcheck // wrapped by the caller
	}
	return v, dialect.FromJSON(roles, &v.Roles)
}

func (p projectStore) PutMember(ctx context.Context, m model.ProjectMember) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, m.ProjectKey); err != nil {
			return err
		}
		roles, err := dialect.ToJSON(m.Roles)
		if err != nil {
			return err
		}
		_, err = p.s.Exec(
			ctx,
			"put member "+m.Name,
			dialect.Upsert(
				"project_members",
				dialect.OpsCols(memberCols),
				[]string{"project_key", "principal_type", "principal_name"},
			),
			m.ProjectKey,
			m.Type,
			m.Name,
			roles,
		)
		if err != nil {
			return err
		}
		return p.rotatePolicy(ctx, m.ProjectKey)
	})
}

func (p projectStore) Member(
	ctx context.Context, projectKey string, typ model.PrincipalType, name string,
) (model.ProjectMember, error) {
	if err := p.guard(ctx); err != nil {
		return model.ProjectMember{}, err
	}
	v, err := scanMember(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind("SELECT "+memberCols+
		" FROM project_members WHERE project_key = ? AND principal_type = ? AND principal_name = ?"),
		projectKey, typ, name))
	return v, p.s.Wrap(fmt.Sprintf("get member %s/%s/%s", projectKey, typ, name), err)
}

func (p projectStore) Members(
	ctx context.Context, projectKey string, typ model.PrincipalType,
) ([]model.ProjectMember, error) {
	if err := p.guard(ctx); err != nil {
		return nil, err
	}
	var out []model.ProjectMember
	err := p.s.Rows(ctx, "list members", "SELECT "+memberCols+
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
	typ model.PrincipalType,
	name string,
) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.s.ExecOne(
			ctx,
			fmt.Sprintf("delete member %s/%s/%s", projectKey, typ, name),
			"DELETE FROM project_members WHERE project_key = ? AND principal_type = ? AND principal_name = ?",
			projectKey,
			typ,
			name,
		); err != nil {
			return err
		}
		return p.rotatePolicy(ctx, projectKey)
	})
}

const assignmentCols = "repo_key, project_key, environments, shared_with_all, shared_read_only, updated_at"

func (p projectStore) PutAssignment(ctx context.Context, a model.RepositoryAssignment) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if err := p.requireProject(ctx, a.ProjectKey); err != nil {
			return err
		}
		envs, err := dialect.ToJSON(a.Environments)
		if err != nil {
			return err
		}
		if _, err := p.s.Exec(
			ctx,
			"put assignment "+a.RepoKey,
			dialect.Upsert(
				"project_repositories",
				append(dialect.OpsCols(assignmentCols), "sharing_revision"),
				[]string{"repo_key"},
			),
			a.RepoKey,
			a.ProjectKey,
			envs,
			a.SharedWithAll,
			a.SharedReadOnly,
			dialect.Micros(a.Updated),
			port.NewSharingAuthority().Revision,
		); err != nil {
			return err
		}
		if _, err := p.s.Exec(ctx, "put assignment "+a.RepoKey,
			"DELETE FROM project_repository_shares WHERE repo_key = ?", a.RepoKey); err != nil {
			return err
		}
		for _, k := range uniqueShares(a.SharedWith) {
			if _, err := p.s.Exec(
				ctx,
				"share "+a.RepoKey,
				dialect.Insert(
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
) ([]model.RepositoryAssignment, error) {
	if err := p.guard(ctx); err != nil {
		return nil, err
	}
	var out []model.RepositoryAssignment
	err := p.s.Rows(ctx, "list assignments",
		"SELECT "+assignmentCols+" FROM project_repositories "+where+" ORDER BY repo_key"+p.s.LockClause(ctx), args,
		func(r *sql.Rows) error {
			var (
				v       model.RepositoryAssignment
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
				return err //nolint:wrapcheck // wrapped by Rows
			}
			v.Updated = dialect.FromMicros(updated)
			err := dialect.FromJSON(envs, &v.Environments)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, err
	}
	// Shares are read after the listing is closed: an in-memory SQLite
	// database has one connection, which the open listing would hold.
	for i := range out {
		err := p.s.Rows(
			ctx,
			"list shares",
			"SELECT project_key FROM project_repository_shares WHERE repo_key = ? ORDER BY project_key",
			[]any{out[i].RepoKey},
			func(r *sql.Rows) error {
				var k string
				err := r.Scan(&k)
				out[i].SharedWith = append(out[i].SharedWith, k)
				return err //nolint:wrapcheck // wrapped by Rows
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
) (model.RepositoryAssignment, error) {
	got, err := p.assignments(ctx, "WHERE repo_key = ?", repoKey)
	if err != nil {
		return model.RepositoryAssignment{}, err
	}
	if len(got) == 0 {
		return model.RepositoryAssignment{}, fmt.Errorf(
			"get assignment %s: %w",
			repoKey,
			kernel.ErrNotFound,
		)
	}
	return got[0], nil
}

func (p projectStore) Assignments(
	ctx context.Context,
	projectKey string,
) ([]model.RepositoryAssignment, error) {
	return p.assignments(ctx, "WHERE project_key = ? OR shared_with_all = ? OR repo_key IN "+
		"(SELECT repo_key FROM project_repository_shares WHERE project_key = ?)", projectKey, true, projectKey)
}

func (p projectStore) DeleteAssignment(ctx context.Context, repoKey string) error {
	return p.tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.Exec(ctx, "delete assignment "+repoKey,
			"DELETE FROM project_repository_shares WHERE repo_key = ?", repoKey); err != nil {
			return err
		}
		return p.s.ExecOne(ctx, "delete assignment "+repoKey,
			"DELETE FROM project_repositories WHERE repo_key = ?", repoKey)
	})
}

func (p projectStore) ForPrincipal(ctx context.Context, user string, groups []string) ([]string, error) {
	if err := p.guard(ctx); err != nil {
		return nil, err
	}
	var conditions []string
	var args []any
	if user != "" {
		conditions = append(conditions, "(principal_type = ? AND principal_name = ?)")
		args = append(args, string(model.PrincipalUser), user)
	}
	if len(groups) > 0 {
		conditions = append(conditions, "(principal_type = ? AND principal_name IN ("+dialect.Placeholders(len(groups))+"))")
		args = append(args, string(model.PrincipalGroup))
		for _, group := range groups {
			args = append(args, group)
		}
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	var result []string
	err := p.s.Rows(
		ctx,
		"projects for principal",
		"SELECT DISTINCT project_key FROM project_members WHERE "+strings.Join(conditions, " OR ")+" ORDER BY project_key",
		args,
		func(rows *sql.Rows) error {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			result = append(result, key)
			return nil
		},
	)
	return result, err
}
