package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
)

// Users implements store.Store.
func (s *Store) Users() store.UserStore { return users{s} }

// Groups implements store.Store.
func (s *Store) Groups() store.GroupStore { return groups{s} }

// Permissions implements store.Store.
func (s *Store) Permissions() store.PermissionStore { return permissions{s} }

// nameKeyset is the keyset of lists ordered by a name column.
var nameKeyset = []string{"name"}

// requireRows returns ErrNotFound naming the first of names absent from table's
// key column, or nil when all exist.
func (s *Store) requireRows(ctx context.Context, table, col, what string, names []string) error {
	for _, n := range names {
		ok, err := s.exists(ctx, "find "+what, "SELECT 1 FROM "+table+" WHERE "+col+" = ?", n)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %q: %w", what, n, store.ErrNotFound)
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
	err := s.rows(ctx, "list memberships",
		"SELECT "+col+", "+other+" FROM group_members WHERE "+col+" IN ("+Placeholders(len(keys))+
			") ORDER BY "+col+", "+other, args,
		func(r *sql.Rows) error {
			var k, v string
			if err := r.Scan(&k, &v); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			out[k] = append(out[k], v)
			return nil
		})
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
		if _, err := s.exec(
			ctx,
			"add member",
			InsertIgnore(
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
	"disable_ui_access, realm, status, mfa, last_logged_in_at, created_at, roles"

func userArgs(u types.User) []any {
	var hash any
	if len(u.PasswordHash) > 0 {
		hash = u.PasswordHash
	}
	return []any{
		u.Name, u.Email, hash, u.Admin, u.ProfileUpdatable, u.InternalPasswordDisabled,
		u.DisableUIAccess, u.Realm, string(u.Status), string(u.MFA),
		micros(u.LastLoggedIn), micros(u.Created), mustJSON(u.Roles),
	}
}

func scanUser(row interface{ Scan(...any) error }) (types.User, error) {
	var (
		u                 types.User
		status, mfa       string
		lastLogin, create sql.NullInt64
		roles             sql.NullString
	)
	if err := row.Scan(&u.Name, &u.Email, &u.PasswordHash, &u.Admin, &u.ProfileUpdatable,
		&u.InternalPasswordDisabled, &u.DisableUIAccess, &u.Realm, &status, &mfa,
		&lastLogin, &create, &roles); err != nil {
		return u, err //nolint:wrapcheck // wrapped by the caller
	}
	u.Status, u.MFA = types.UserStatus(status), types.MFAStatus(mfa)
	u.LastLoggedIn, u.Created = fromMicros(lastLogin), fromMicros(create)
	if len(u.PasswordHash) == 0 {
		u.PasswordHash = nil
	}
	return u, fromJSON(roles, &u.Roles)
}

func (us users) Create(ctx context.Context, u types.User) error {
	args := userArgs(u)
	return us.s.Tx(ctx, func(ctx context.Context) error {
		groups := sortedUnique(u.Groups)
		if err := us.s.requireRows(ctx, "user_groups", "name", "group", groups); err != nil {
			return err
		}
		if _, err := us.s.exec(ctx, "create user "+u.Name,
			Insert("users", strings.Split(userCols, ", ")), args...); err != nil {
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

func (us users) Get(ctx context.Context, name string) (types.User, error) {
	u, err := scanUser(us.s.q(ctx).QueryRowContext(ctx,
		us.s.d.Rebind("SELECT "+userCols+" FROM users WHERE name = ?"), name))
	if err != nil {
		return u, us.s.wrap(fmt.Sprintf("get user %q", name), err)
	}
	m, err := us.s.memberships(ctx, "user_name", "group_name", []string{name})
	u.Groups = m[name]
	return u, err
}

func (us users) Update(ctx context.Context, u types.User) error {
	args := userArgs(u)
	return us.s.Tx(ctx, func(ctx context.Context) error {
		groups := sortedUnique(u.Groups)
		if err := us.s.requireRows(ctx, "user_groups", "name", "group", groups); err != nil {
			return err
		}
		cols := strings.Split(userCols, ", ")[1:]
		if err := us.s.execOne(ctx, fmt.Sprintf("update user %q", u.Name),
			"UPDATE users SET "+strings.Join(cols, " = ?, ")+" = ? WHERE name = ?",
			append(args[1:], u.Name)...); err != nil {
			return err
		}
		if _, err := us.s.exec(
			ctx,
			"clear groups",
			"DELETE FROM group_members WHERE user_name = ?",
			u.Name,
		); err != nil {
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
		if _, err := us.s.exec(
			ctx,
			"delete memberships",
			"DELETE FROM group_members WHERE user_name = ?",
			name,
		); err != nil {
			return err
		}
		return us.s.execOne(
			ctx,
			fmt.Sprintf("delete user %q", name),
			"DELETE FROM users WHERE name = ?",
			name,
		)
	})
}

func (us users) List(ctx context.Context, p store.Page) ([]types.User, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, nameKeyset, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []types.User
	err = us.s.rows(ctx, "list users",
		"SELECT "+userCols+" FROM users WHERE "+cond+orderBy(nameKeyset, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			u, err := scanUser(r)
			out = append(out, u)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(u types.User) []string { return []string{u.Name} })
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
	return us.s.execOne(ctx, fmt.Sprintf("record login %q", name),
		"UPDATE users SET last_logged_in_at = ? WHERE name = ?", micros(at), name)
}

// --- Groups ----------------------------------------------------------------

type groups struct{ s *Store }

const groupCols = "name, description, auto_join, admin_privileges, realm, realm_attributes, external_id, roles"

func groupArgs(g types.Group) []any {
	return []any{
		g.Name, g.Description, g.AutoJoin, g.AdminPrivileges, g.Realm, g.RealmAttributes,
		g.ExternalID, mustJSON(g.Roles),
	}
}

func scanGroup(row interface{ Scan(...any) error }) (types.Group, error) {
	var (
		g     types.Group
		roles sql.NullString
	)
	if err := row.Scan(&g.Name, &g.Description, &g.AutoJoin, &g.AdminPrivileges, &g.Realm,
		&g.RealmAttributes, &g.ExternalID, &roles); err != nil {
		return g, err //nolint:wrapcheck // wrapped by the caller
	}
	return g, fromJSON(roles, &g.Roles)
}

func (gs groups) Create(ctx context.Context, g types.Group) error {
	args := groupArgs(g)
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		members := sortedUnique(g.Members)
		if err := gs.s.requireRows(ctx, "users", "name", "user", members); err != nil {
			return err
		}
		if _, err := gs.s.exec(ctx, "create group "+g.Name,
			Insert("user_groups", strings.Split(groupCols, ", ")), args...); err != nil {
			return err
		}
		return gs.s.addMembers(ctx, g.Name, members)
	})
}

func (gs groups) Get(ctx context.Context, name string) (types.Group, error) {
	g, err := scanGroup(gs.s.q(ctx).QueryRowContext(ctx,
		gs.s.d.Rebind("SELECT "+groupCols+" FROM user_groups WHERE name = ?"), name))
	if err != nil {
		return g, gs.s.wrap(fmt.Sprintf("get group %q", name), err)
	}
	m, err := gs.s.memberships(ctx, "group_name", "user_name", []string{name})
	g.Members = m[name]
	return g, err
}

func (gs groups) Update(ctx context.Context, g types.Group) error {
	args := groupArgs(g)
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		members := sortedUnique(g.Members)
		if err := gs.s.requireRows(ctx, "users", "name", "user", members); err != nil {
			return err
		}
		cols := strings.Split(groupCols, ", ")[1:]
		if err := gs.s.execOne(ctx, fmt.Sprintf("update group %q", g.Name),
			"UPDATE user_groups SET "+strings.Join(cols, " = ?, ")+" = ? WHERE name = ?",
			append(args[1:], g.Name)...); err != nil {
			return err
		}
		if _, err := gs.s.exec(
			ctx,
			"clear members",
			"DELETE FROM group_members WHERE group_name = ?",
			g.Name,
		); err != nil {
			return err
		}
		return gs.s.addMembers(ctx, g.Name, members)
	})
}

func (gs groups) Delete(ctx context.Context, name string) error {
	return gs.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := gs.s.exec(
			ctx,
			"delete members",
			"DELETE FROM group_members WHERE group_name = ?",
			name,
		); err != nil {
			return err
		}
		return gs.s.execOne(
			ctx,
			fmt.Sprintf("delete group %q", name),
			"DELETE FROM user_groups WHERE name = ?",
			name,
		)
	})
}

func (gs groups) List(ctx context.Context, p store.Page) ([]types.Group, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, nameKeyset, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []types.Group
	err = gs.s.rows(ctx, "list groups",
		"SELECT "+groupCols+" FROM user_groups WHERE "+cond+orderBy(nameKeyset, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			g, err := scanGroup(r)
			out = append(out, g)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(g types.Group) []string { return []string{g.Name} })
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
			if _, err := gs.s.exec(
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
	err := gs.s.rows(ctx, "list auto-join groups",
		"SELECT name FROM user_groups WHERE auto_join = ? ORDER BY name", []any{true},
		func(r *sql.Rows) error {
			var n string
			err := r.Scan(&n)
			out = append(out, n)
			return err //nolint:wrapcheck // wrapped by rows
		})
	return out, err
}

// --- Permissions -----------------------------------------------------------

type permissions struct{ s *Store }

const permissionCols = "name, description, created_at, updated_at, resources"

func scanPermission(row interface{ Scan(...any) error }) (types.Permission, error) {
	var (
		p                types.Permission
		created, updated sql.NullInt64
		resources        sql.NullString
	)
	if err := row.Scan(&p.Name, &p.Description, &created, &updated, &resources); err != nil {
		return p, err //nolint:wrapcheck // wrapped by the caller
	}
	p.Created, p.Updated = fromMicros(created), fromMicros(updated)
	return p, fromJSON(resources, &p.Resources)
}

// mirror replaces the grant and target rows of p, which ForPrincipal and
// WithTarget query.
func (ps permissions) mirror(ctx context.Context, p types.Permission) error {
	for _, table := range []string{"permission_grants", "permission_targets"} {
		if _, err := ps.s.exec(ctx, "clear "+table,
			"DELETE FROM "+table+" WHERE permission_name = ?", p.Name); err != nil {
			return err
		}
	}
	for _, rt := range p.ResourceTypes() {
		res := p.Resources[rt]
		for _, principal := range []struct {
			typ    types.PrincipalType
			grants map[string]types.Actions
		}{{types.PrincipalUser, res.Actions.Users}, {types.PrincipalGroup, res.Actions.Groups}} {
			for name, a := range principal.grants {
				if _, err := ps.s.exec(ctx, "mirror grant", Insert(
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
			if _, err := ps.s.exec(
				ctx,
				"mirror target",
				Insert(
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
				mustJSON(tg.Include),
				mustJSON(tg.Exclude),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ps permissions) Create(ctx context.Context, p types.Permission) error {
	return ps.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := ps.s.exec(
			ctx,
			"create permission "+p.Name,
			Insert("permissions", strings.Split(permissionCols, ", ")),
			p.Name,
			p.Description,
			micros(p.Created),
			micros(p.Updated),
			mustJSON(p.Resources),
		); err != nil {
			return err
		}
		return ps.mirror(ctx, p)
	})
}

func (ps permissions) Get(ctx context.Context, name string) (types.Permission, error) {
	p, err := scanPermission(ps.s.q(ctx).QueryRowContext(ctx,
		ps.s.d.Rebind("SELECT "+permissionCols+" FROM permissions WHERE name = ?"), name))
	return p, ps.s.wrap(fmt.Sprintf("get permission %q", name), err)
}

func (ps permissions) Update(ctx context.Context, p types.Permission) error {
	return ps.s.Tx(ctx, func(ctx context.Context) error {
		if err := ps.s.execOne(
			ctx,
			fmt.Sprintf("update permission %q", p.Name),
			"UPDATE permissions SET description = ?, created_at = ?, updated_at = ?, resources = ? WHERE name = ?",
			p.Description,
			micros(p.Created),
			micros(p.Updated),
			mustJSON(p.Resources),
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
			if _, err := ps.s.exec(ctx, "clear "+table,
				"DELETE FROM "+table+" WHERE permission_name = ?", name); err != nil {
				return err
			}
		}
		return ps.s.execOne(ctx, fmt.Sprintf("delete permission %q", name),
			"DELETE FROM permissions WHERE name = ?", name)
	})
}

// query lists the permissions matching where, ordered by name, with tail
// (a LIMIT clause or "") appended.
func (ps permissions) query(
	ctx context.Context,
	op, where, tail string,
	args []any,
) ([]types.Permission, error) {
	var out []types.Permission
	err := ps.s.rows(
		ctx,
		op,
		"SELECT "+permissionCols+" FROM permissions WHERE "+where+orderBy(
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

func (ps permissions) List(ctx context.Context, p store.Page) ([]types.Permission, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, nameKeyset, nil, false)
	if err != nil {
		return nil, "", err
	}
	out, err := ps.query(ctx, "list permissions", cond, " LIMIT ?", append(args, p.Limit+1))
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(v types.Permission) []string { return []string{v.Name} },
	)
	return page, next, nil
}

func (ps permissions) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]types.Permission, error) {
	where := []string{"(principal_type = ? AND principal_name = ?)"}
	args := []any{string(types.PrincipalUser), user}
	if len(groups) > 0 {
		where = append(
			where,
			"(principal_type = ? AND principal_name IN ("+Placeholders(len(groups))+"))",
		)
		args = append(args, string(types.PrincipalGroup))
		for _, g := range groups {
			args = append(args, g)
		}
	}
	return ps.query(ctx, "permissions for principal",
		"name IN (SELECT permission_name FROM permission_grants WHERE actions <> 0 AND ("+
			strings.Join(where, " OR ")+"))", "", args)
}

func (ps permissions) WithTarget(ctx context.Context, target string) ([]types.Permission, error) {
	return ps.query(
		ctx,
		"permissions with target",
		"name IN (SELECT permission_name FROM permission_targets WHERE target = ?)",
		"",
		[]any{target},
	)
}
