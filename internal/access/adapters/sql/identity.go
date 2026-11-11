package sql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Users returns the user store: users, their credentials and their
// group memberships (decision 0005).
func (s *Store) Users() port.UserStore { return users{s} }

// Groups returns the group store: groups and their members.
func (s *Store) Groups() port.GroupStore { return groups{s} }

// Permissions returns the permission store: Access v2 permissions, with
// their grants and targets mirrored for lookup by principal and by
// target (decision 0006).
func (s *Store) Permissions() port.PermissionStore { return permissions{s} }

// nameKeyset is the keyset of lists ordered by a name column.
var nameKeyset = []string{"name"}

// requireRows returns ErrNotFound naming the first of names absent from table's
// key column, or nil when all exist.
func (s *Store) requireRows(ctx context.Context, table, col, what string, names []string) error {
	for _, n := range names {
		ok, err := s.Exists(ctx, "find "+what, "SELECT 1 FROM "+table+" WHERE "+col+" = ?", n)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %q: %w", what, n, kernel.ErrNotFound)
		}
	}
	return nil
}

// memberships returns, for each of keys, the sorted values of other in the
// group_members rows whose col is that key: a user's groups or a group's
// members.
func (s *Store) memberships(
	ctx context.Context,
	col, other string,
	keys []string,
) (map[string][]string, error) {
	out := make(map[string][]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	err := s.Rows(
		ctx,
		"list memberships",
		"SELECT "+col+", "+other+" FROM group_members WHERE "+col+" IN ("+dialect.Placeholders(
			len(keys),
		)+
			") ORDER BY "+col+", "+other,
		args,
		func(r *sql.Rows) error {
			var k, v string
			if err := r.Scan(&k, &v); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out[k] = append(out[k], v)
			return nil
		},
	)
	return out, err
}

// sortedUnique returns names sorted and without repeats.
func sortedUnique(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// addMembers inserts group_members rows, ignoring existing ones.
func (s *Store) addMembers(ctx context.Context, group string, users []string) error {
	for _, u := range users {
		if _, err := s.Exec(
			ctx,
			"add member",
			dialect.InsertIgnore(
				"group_members",
				[]string{"group_name", "user_name"},
				[]string{"group_name", "user_name"},
			),
			group,
			u,
		); err != nil {
			return err
		}
	}
	return nil
}

// --- Users -----------------------------------------------------------------

type users struct{ s *Store }

const userCols = "name, email, password_hash, admin, profile_updatable, internal_password_disabled, " +
	"disable_ui_access, realm, status, mfa, last_logged_in_at, created_at, roles, " +
	"failed_logins, password_changed_at, password_expired"

func userArgs(u model.User) []any {
	var hash any
	if len(u.PasswordHash) > 0 {
		hash = u.PasswordHash
	}
	return []any{
		u.Name, u.Email, hash, u.Admin, u.ProfileUpdatable, u.InternalPasswordDisabled,
		u.DisableUIAccess, u.Realm, string(u.Status), string(u.MFA),
		dialect.Micros(u.LastLoggedIn), dialect.Micros(u.Created), dialect.MustJSON(u.Roles),
		u.FailedLogins, dialect.Micros(u.PasswordChanged), u.PasswordExpired,
	}
}

func scanUser(row interface{ Scan(...any) error }) (model.User, error) {
	var (
		u                 model.User
		status, mfa       string
		lastLogin, create sql.NullInt64
		changed           sql.NullInt64
		roles             sql.NullString
	)
	if err := row.Scan(&u.Name, &u.Email, &u.PasswordHash, &u.Admin, &u.ProfileUpdatable,
		&u.InternalPasswordDisabled, &u.DisableUIAccess, &u.Realm, &status, &mfa,
		&lastLogin, &create, &roles, &u.FailedLogins, &changed, &u.PasswordExpired); err != nil {
		return u, err //nolint:wrapcheck // wrapped by the caller
	}
	u.Status, u.MFA = model.UserStatus(status), model.MFAStatus(mfa)
	u.LastLoggedIn, u.Created = dialect.FromMicros(lastLogin), dialect.FromMicros(create)
	u.PasswordChanged = dialect.FromMicros(changed)
	if len(u.PasswordHash) == 0 {
		u.PasswordHash = nil
	}
	return u, dialect.FromJSON(roles, &u.Roles)
}

func (us users) Create(ctx context.Context, u model.User) error {
	args := userArgs(u)
	return us.s.Tx(ctx, func(ctx context.Context) error {
		groups := sortedUnique(u.Groups)
		if err := us.s.requireRows(ctx, "user_groups", "name", "group", groups); err != nil {
			return err
		}
		if _, err := us.s.Exec(ctx, "create user "+u.Name,
			dialect.Insert("users", strings.Split(userCols, ", ")), args...); err != nil {
			return err
		}
		if err := us.s.writeIdentitySearch(ctx, "users", u.Name); err != nil {
			return err
		}
		for _, g := range groups {
			if err := us.s.addMembers(ctx, g, []string{u.Name}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (us users) Get(ctx context.Context, name string) (model.User, error) {
	u, err := scanUser(us.s.Q(ctx).QueryRowContext(ctx,
		us.s.Dialect().Rebind("SELECT "+userCols+" FROM users WHERE name = ?"), name))
	if err != nil {
		return u, us.s.Wrap(fmt.Sprintf("get user %q", name), err)
	}
	m, err := us.s.memberships(ctx, "user_name", "group_name", []string{name})
	u.Groups = m[name]
	return u, err
}

func (us users) Update(ctx context.Context, u model.User) error {
	args := userArgs(u)
	return us.s.Tx(ctx, func(ctx context.Context) error {
		groups := sortedUnique(u.Groups)
		if err := us.s.requireRows(ctx, "user_groups", "name", "group", groups); err != nil {
			return err
		}
		cols := strings.Split(userCols, ", ")[1:]
		if err := us.s.ExecOne(ctx, fmt.Sprintf("update user %q", u.Name),
			"UPDATE users SET "+strings.Join(cols, " = ?, ")+" = ? WHERE name = ?",
			append(args[1:], u.Name)...); err != nil {
			return err
		}
		if _, err := us.s.Exec(
			ctx,
			"clear groups",
			"DELETE FROM group_members WHERE user_name = ?",
			u.Name,
		); err != nil {
			return err
		}
		if err := us.s.writeIdentitySearch(ctx, "users", u.Name); err != nil {
			return err
		}
		for _, g := range groups {
			if err := us.s.addMembers(ctx, g, []string{u.Name}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (us users) Delete(ctx context.Context, name string) error {
	return us.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := us.s.Exec(
			ctx,
			"delete memberships",
			"DELETE FROM group_members WHERE user_name = ?",
			name,
		); err != nil {
			return err
		}
		return us.s.ExecOne(
			ctx,
			fmt.Sprintf("delete user %q", name),
			"DELETE FROM users WHERE name = ?",
			name,
		)
	})
}

func (us users) List(ctx context.Context, p kernel.Page) ([]model.User, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, nameKeyset, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []model.User
	err = us.s.Rows(ctx, "list users",
		"SELECT "+userCols+" FROM users WHERE "+cond+dialect.OrderBy(nameKeyset, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			u, err := scanUser(r)
			out = append(out, u)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(out, p.Limit, func(u model.User) []string { return []string{u.Name} })
	names := make([]string, len(page))
	for i, u := range page {
		names[i] = u.Name
	}
	m, err := us.s.memberships(ctx, "user_name", "group_name", names)
	if err != nil {
		return nil, "", err
	}
	for i := range page {
		page[i].Groups = m[page[i].Name]
	}
	return page, next, nil
}

func (us users) RecordLogin(ctx context.Context, name string, at time.Time) error {
	return us.s.ExecOne(
		ctx,
		fmt.Sprintf("record login %q", name),
		"UPDATE users SET last_logged_in_at = ?, failed_logins = 0 WHERE name = ?",
		dialect.Micros(at),
		name,
	)
}

// RecordFailedLogin counts and locks in one statement, so concurrent wrong
// passwords cannot each read the old count and all stay below the limit.
func (us users) RecordFailedLogin(ctx context.Context, name string, lockAt int) (bool, error) {
	var locked bool
	err := us.s.Tx(ctx, func(ctx context.Context) error {
		if err := us.s.ExecOne(ctx, fmt.Sprintf("record failed login %q", name),
			"UPDATE users SET failed_logins = failed_logins + 1, status = CASE "+
				"WHEN ? > 0 AND failed_logins + 1 >= ? AND status IN ('', ?) THEN ? "+
				"ELSE status END WHERE name = ?",
			lockAt, lockAt, string(model.UserEnabled), string(model.UserLocked), name); err != nil {
			return err
		}
		var status string
		if err := us.s.Q(ctx).QueryRowContext(ctx,
			us.s.Dialect().Rebind("SELECT status FROM users WHERE name = ?"),
			name).
			Scan(&status); err != nil {
			return us.s.Wrap(fmt.Sprintf("read user %q", name), err)
		}
		locked = status == string(model.UserLocked)
		return nil
	})
	return locked, err
}

func (us users) Unlock(ctx context.Context, names ...string) error {
	unlock := "UPDATE users SET failed_logins = 0, status = CASE WHEN status = ? THEN ? ELSE status END"
	args := []any{string(model.UserLocked), string(model.UserEnabled)}
	if len(names) == 0 {
		_, err := us.s.Exec(
			ctx,
			"unlock users",
			unlock+" WHERE status = ? OR failed_logins > 0",
			append(args, string(model.UserLocked))...)
		return err
	}
	for _, n := range names {
		args = append(args, n)
	}
	_, err := us.s.Exec(ctx, "unlock users",
		unlock+" WHERE name IN ("+dialect.Placeholders(len(names))+")", args...)
	return err
}

func (us users) Locked(ctx context.Context) ([]string, error) {
	out := []string{}
	err := us.s.Rows(ctx, "list locked users",
		"SELECT name FROM users WHERE status = ? ORDER BY name", []any{string(model.UserLocked)},
		func(r *sql.Rows) error {
			var n string
			if err := r.Scan(&n); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out = append(out, n)
			return nil
		})
	return out, err
}

// --- Groups ----------------------------------------------------------------

type groups struct{ s *Store }

const groupCols = "name, description, auto_join, admin_privileges, realm, realm_attributes, external_id, roles"

func groupArgs(g model.Group) []any {
	return []any{
		g.Name, g.Description, g.AutoJoin, g.AdminPrivileges, g.Realm, g.RealmAttributes,
		g.ExternalID, dialect.MustJSON(g.Roles),
	}
}

func scanGroup(row interface{ Scan(...any) error }) (model.Group, error) {
	var (
		g     model.Group
		roles sql.NullString
	)
	if err := row.Scan(&g.Name, &g.Description, &g.AutoJoin, &g.AdminPrivileges, &g.Realm,
		&g.RealmAttributes, &g.ExternalID, &roles); err != nil {
		return g, err //nolint:wrapcheck // wrapped by the caller
	}
	return g, dialect.FromJSON(roles, &g.Roles)
}

func (gs groups) Create(ctx context.Context, g model.Group) error {
	args := groupArgs(g)
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		members := sortedUnique(g.Members)
		if err := gs.s.requireRows(ctx, "users", "name", "user", members); err != nil {
			return err
		}
		if _, err := gs.s.Exec(ctx, "create group "+g.Name,
			dialect.Insert("user_groups", strings.Split(groupCols, ", ")), args...); err != nil {
			return err
		}
		if err := gs.s.writeIdentitySearch(ctx, "user_groups", g.Name); err != nil {
			return err
		}
		return gs.s.addMembers(ctx, g.Name, members)
	})
}

func (gs groups) Get(ctx context.Context, name string) (model.Group, error) {
	g, err := scanGroup(gs.s.Q(ctx).QueryRowContext(ctx,
		gs.s.Dialect().Rebind("SELECT "+groupCols+" FROM user_groups WHERE name = ?"), name))
	if err != nil {
		return g, gs.s.Wrap(fmt.Sprintf("get group %q", name), err)
	}
	m, err := gs.s.memberships(ctx, "group_name", "user_name", []string{name})
	g.Members = m[name]
	return g, err
}

func (gs groups) Update(ctx context.Context, g model.Group) error {
	args := groupArgs(g)
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		members := sortedUnique(g.Members)
		if err := gs.s.requireRows(ctx, "users", "name", "user", members); err != nil {
			return err
		}
		cols := strings.Split(groupCols, ", ")[1:]
		if err := gs.s.ExecOne(ctx, fmt.Sprintf("update group %q", g.Name),
			"UPDATE user_groups SET "+strings.Join(cols, " = ?, ")+" = ? WHERE name = ?",
			append(args[1:], g.Name)...); err != nil {
			return err
		}
		if _, err := gs.s.Exec(
			ctx,
			"clear members",
			"DELETE FROM group_members WHERE group_name = ?",
			g.Name,
		); err != nil {
			return err
		}
		if err := gs.s.writeIdentitySearch(ctx, "user_groups", g.Name); err != nil {
			return err
		}
		return gs.s.addMembers(ctx, g.Name, members)
	})
}

func (gs groups) Delete(ctx context.Context, name string) error {
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := gs.s.Exec(
			ctx,
			"delete members",
			"DELETE FROM group_members WHERE group_name = ?",
			name,
		); err != nil {
			return err
		}
		return gs.s.ExecOne(
			ctx,
			fmt.Sprintf("delete group %q", name),
			"DELETE FROM user_groups WHERE name = ?",
			name,
		)
	})
}

func (gs groups) List(ctx context.Context, p kernel.Page) ([]model.Group, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, nameKeyset, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []model.Group
	err = gs.s.Rows(ctx, "list groups",
		"SELECT "+groupCols+" FROM user_groups WHERE "+cond+dialect.OrderBy(nameKeyset, false)+
			" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			g, err := scanGroup(r)
			out = append(out, g)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(g model.Group) []string { return []string{g.Name} },
	)
	names := make([]string, len(page))
	for i, g := range page {
		names[i] = g.Name
	}
	m, err := gs.s.memberships(ctx, "group_name", "user_name", names)
	if err != nil {
		return nil, "", err
	}
	for i := range page {
		page[i].Members = m[page[i].Name]
	}
	return page, next, nil
}

func (gs groups) AddMembers(ctx context.Context, group string, users ...string) error {
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		if err := gs.s.requireRows(
			ctx,
			"user_groups",
			"name",
			"group",
			[]string{group},
		); err != nil {
			return err
		}
		users = sortedUnique(users)
		if err := gs.s.requireRows(ctx, "users", "name", "user", users); err != nil {
			return err
		}
		return gs.s.addMembers(ctx, group, users)
	})
}

func (gs groups) RemoveMembers(ctx context.Context, group string, users ...string) error {
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		if err := gs.s.requireRows(
			ctx,
			"user_groups",
			"name",
			"group",
			[]string{group},
		); err != nil {
			return err
		}
		for _, u := range users {
			if _, err := gs.s.Exec(
				ctx,
				"remove member",
				"DELETE FROM group_members WHERE group_name = ? AND user_name = ?",
				group,
				u,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (gs groups) AutoJoin(ctx context.Context) ([]string, error) {
	var out []string
	err := gs.s.Rows(ctx, "list auto-join groups",
		"SELECT name FROM user_groups WHERE auto_join = ? ORDER BY name", []any{true},
		func(r *sql.Rows) error {
			var n string
			err := r.Scan(&n)
			out = append(out, n)
			return err //nolint:wrapcheck // wrapped by Rows
		})
	return out, err
}

// --- Permissions -----------------------------------------------------------

type permissions struct{ s *Store }

const permissionCols = "name, description, created_at, updated_at, resources"

func scanPermission(row interface{ Scan(...any) error }) (model.Permission, error) {
	var (
		p                model.Permission
		created, updated sql.NullInt64
		resources        sql.NullString
	)
	if err := row.Scan(&p.Name, &p.Description, &created, &updated, &resources); err != nil {
		return p, err //nolint:wrapcheck // wrapped by the caller
	}
	p.Created, p.Updated = dialect.FromMicros(created), dialect.FromMicros(updated)
	return p, dialect.FromJSON(resources, &p.Resources)
}

// mirror replaces the grant and target rows of p, which ForPrincipal and
// WithTarget query.
func (ps permissions) mirror(ctx context.Context, p model.Permission) error {
	for _, table := range []string{"permission_grants", "permission_targets"} {
		if _, err := ps.s.Exec(ctx, "clear "+table,
			"DELETE FROM "+table+" WHERE permission_name = ?", p.Name); err != nil {
			return err
		}
	}
	for _, rt := range p.ResourceTypes() {
		res := p.Resources[rt]
		for _, principal := range []struct {
			typ    model.PrincipalType
			grants map[string]api.Actions
		}{{model.PrincipalUser, res.Actions.Users}, {model.PrincipalGroup, res.Actions.Groups}} {
			for name, a := range principal.grants {
				if _, err := ps.s.Exec(ctx, "mirror grant", dialect.Insert(
					"permission_grants",
					[]string{
						"permission_name",
						"resource_type",
						"principal_type",
						"principal_name",
						"actions",
					},
				),
					p.Name, string(rt), string(principal.typ), name, int64(a)); err != nil {
					return err
				}
			}
		}
		for target, tg := range res.Targets {
			if _, err := ps.s.Exec(
				ctx,
				"mirror target",
				dialect.Insert(
					"permission_targets",
					[]string{
						"permission_name",
						"resource_type",
						"target",
						"include_patterns",
						"exclude_patterns",
					},
				),
				p.Name,
				string(rt),
				target,
				dialect.MustJSON(tg.Include),
				dialect.MustJSON(tg.Exclude),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ps permissions) Create(ctx context.Context, p model.Permission) error {
	return ps.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := ps.s.Exec(
			ctx,
			"create permission "+p.Name,
			dialect.Insert("permissions", strings.Split(permissionCols, ", ")),
			p.Name,
			p.Description,
			dialect.Micros(p.Created),
			dialect.Micros(p.Updated),
			dialect.MustJSON(p.Resources),
		); err != nil {
			return err
		}
		return ps.mirror(ctx, p)
	})
}

func (ps permissions) Get(ctx context.Context, name string) (model.Permission, error) {
	p, err := scanPermission(ps.s.Q(ctx).QueryRowContext(ctx,
		ps.s.Dialect().Rebind("SELECT "+permissionCols+" FROM permissions WHERE name = ?"), name))
	return p, ps.s.Wrap(fmt.Sprintf("get permission %q", name), err)
}

func (ps permissions) Update(ctx context.Context, p model.Permission) error {
	return ps.s.Tx(ctx, func(ctx context.Context) error {
		if err := ps.s.ExecOne(
			ctx,
			fmt.Sprintf("update permission %q", p.Name),
			"UPDATE permissions SET description = ?, created_at = ?, updated_at = ?, resources = ? WHERE name = ?",
			p.Description,
			dialect.Micros(p.Created),
			dialect.Micros(p.Updated),
			dialect.MustJSON(p.Resources),
			p.Name,
		); err != nil {
			return err
		}
		return ps.mirror(ctx, p)
	})
}

func (ps permissions) Delete(ctx context.Context, name string) error {
	return ps.s.Tx(ctx, func(ctx context.Context) error {
		for _, table := range []string{"permission_grants", "permission_targets"} {
			if _, err := ps.s.Exec(ctx, "clear "+table,
				"DELETE FROM "+table+" WHERE permission_name = ?", name); err != nil {
				return err
			}
		}
		return ps.s.ExecOne(ctx, fmt.Sprintf("delete permission %q", name),
			"DELETE FROM permissions WHERE name = ?", name)
	})
}

// query lists the permissions matching where, ordered by name, with tail
// (a LIMIT clause or "") appended.
func (ps permissions) query(
	ctx context.Context,
	op, where, tail string,
	args []any,
) ([]model.Permission, error) {
	var out []model.Permission
	err := ps.s.Rows(
		ctx,
		op,
		"SELECT "+permissionCols+" FROM permissions WHERE "+where+dialect.OrderBy(
			nameKeyset,
			false,
		)+tail,
		args,
		func(r *sql.Rows) error {
			p, err := scanPermission(r)
			out = append(out, p)
			return err
		},
	)
	return out, err
}

func (ps permissions) List(ctx context.Context, p kernel.Page) ([]model.Permission, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, nameKeyset, nil, false)
	if err != nil {
		return nil, "", err
	}
	out, err := ps.query(ctx, "list permissions", cond, " LIMIT ?", append(args, p.Limit+1))
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(v model.Permission) []string { return []string{v.Name} },
	)
	return page, next, nil
}

func (ps permissions) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]model.Permission, error) {
	where := []string{"(principal_type = ? AND principal_name = ?)"}
	args := []any{string(model.PrincipalUser), user}
	if len(groups) > 0 {
		where = append(
			where,
			"(principal_type = ? AND principal_name IN ("+dialect.Placeholders(len(groups))+"))",
		)
		args = append(args, string(model.PrincipalGroup))
		for _, g := range groups {
			args = append(args, g)
		}
	}
	return ps.query(ctx, "permissions for principal",
		"name IN (SELECT permission_name FROM permission_grants WHERE actions <> 0 AND ("+
			strings.Join(where, " OR ")+"))", "", args)
}

func (ps permissions) WithTarget(ctx context.Context, target string) ([]model.Permission, error) {
	return ps.query(
		ctx,
		"permissions with target",
		"name IN (SELECT permission_name FROM permission_targets WHERE target = ?)",
		"",
		[]any{target},
	)
}
