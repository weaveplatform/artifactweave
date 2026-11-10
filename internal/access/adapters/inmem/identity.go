package inmem

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
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

// membership is a group_members row.
type membership struct{ Group, User string }

func (s *Store) usersT() *memtx.Table[string, model.User] {
	return memtx.TableOf[string, model.User](s.Store, "users")
}

func (s *Store) groupsT() *memtx.Table[string, model.Group] {
	return memtx.TableOf[string, model.Group](s.Store, "user_groups")
}

func (s *Store) membersT() *memtx.Table[membership, struct{}] {
	return memtx.TableOf[membership, struct{}](s.Store, "group_members")
}

// sortedUnique returns names sorted and without repeats.
func sortedUnique(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// requireKeys returns ErrNotFound for the first of names t has no row for.
func requireKeys[V any](t *memtx.Table[string, V], what string, names []string) error {
	for _, n := range names {
		if !t.Has(n) {
			return memtx.NotFound(what, n) //nolint:wrapcheck // wraps kernel.ErrNotFound
		}
	}
	return nil
}

// groupsOf returns a user's groups, sorted.
func (s *Store) groupsOf(user string) []string {
	var out []string
	for m := range s.membersT().Rows {
		if m.User == user {
			out = append(out, m.Group)
		}
	}
	slices.Sort(out)
	return out
}

// membersOf returns a group's members, sorted.
func (s *Store) membersOf(group string) []string {
	var out []string
	for m := range s.membersT().Rows {
		if m.Group == group {
			out = append(out, m.User)
		}
	}
	slices.Sort(out)
	return out
}

func compareByKey[V any](name func(V) string) func(a, b V) int {
	return func(a, b V) int { return strings.Compare(name(a), name(b)) }
}

// --- Users -----------------------------------------------------------------

type users struct{ s *Store }

func userName(u model.User) string { return u.Name }

func (us users) Create(ctx context.Context, u model.User) error {
	return us.s.Do(ctx, func() error {
		if us.s.usersT().Has(u.Name) {
			return memtx.Conflict("user", u.Name)
		}
		return us.write(u)
	})
}

// write stores u and replaces its memberships, after checking its groups.
func (us users) write(u model.User) error {
	groups := sortedUnique(u.Groups)
	if err := requireKeys(us.s.groupsT(), "group", groups); err != nil {
		return err
	}
	u.Groups = nil
	us.s.usersT().Put(u.Name, u)
	us.s.membersT().DeleteWhere(func(m membership, _ struct{}) bool { return m.User == u.Name })
	for _, g := range groups {
		us.s.membersT().Put(membership{g, u.Name}, struct{}{})
	}
	return nil
}

func (us users) Get(ctx context.Context, name string) (model.User, error) {
	var out model.User
	err := us.s.Do(ctx, func() error {
		u, ok := us.s.usersT().Get(name)
		if !ok {
			return memtx.NotFound("user", name)
		}
		u.Groups = us.s.groupsOf(name)
		out = u
		return nil
	})
	return out, err
}

func (us users) Update(ctx context.Context, u model.User) error {
	return us.s.Do(ctx, func() error {
		if !us.s.usersT().Has(u.Name) {
			return memtx.NotFound("user", u.Name)
		}
		return us.write(u)
	})
}

func (us users) Delete(ctx context.Context, name string) error {
	return us.s.Do(ctx, func() error {
		if !us.s.usersT().Del(name) {
			return memtx.NotFound("user", name)
		}
		us.s.membersT().DeleteWhere(func(m membership, _ struct{}) bool { return m.User == name })
		apiRestrictions(us).current().Del(name)
		return nil
	})
}

func (us users) List(ctx context.Context, p kernel.Page) ([]model.User, string, error) {
	var (
		out  []model.User
		next string
	)
	err := us.s.Do(ctx, func() error {
		rows := us.s.usersT().All(nil, compareByKey(userName))
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(u model.User) []any { return []any{u.Name} },
		)
		for i := range out {
			out[i].Groups = us.s.groupsOf(out[i].Name)
		}
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (us users) RecordLogin(ctx context.Context, name string, at time.Time) error {
	return us.s.Do(ctx, func() error {
		u, ok := us.s.usersT().Get(name)
		if !ok {
			return memtx.NotFound("user", name)
		}
		u.LastLoggedIn = at
		u.FailedLogins = 0
		us.s.usersT().Put(name, u)
		return nil
	})
}

func (us users) RecordFailedLogin(ctx context.Context, name string, lockAt int) (bool, error) {
	var locked bool
	err := us.s.Do(ctx, func() error {
		u, ok := us.s.usersT().Get(name)
		if !ok {
			return memtx.NotFound("user", name)
		}
		u.FailedLogins++
		enabled := u.Status == "" || u.Status == model.UserEnabled
		if lockAt > 0 && u.FailedLogins >= lockAt && enabled {
			u.Status = model.UserLocked
		}
		locked = u.Status == model.UserLocked
		us.s.usersT().Put(name, u)
		return nil
	})
	return locked, err
}

func (us users) Unlock(ctx context.Context, names ...string) error {
	return us.s.Do(ctx, func() error {
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		for _, u := range us.s.usersT().All(nil, compareByKey(userName)) {
			switch {
			case len(names) > 0 && !want[u.Name]:
				continue
			case len(names) == 0 && u.Status != model.UserLocked && u.FailedLogins == 0:
				continue
			}
			if u.Status == model.UserLocked {
				u.Status = model.UserEnabled
			}
			u.FailedLogins = 0
			us.s.usersT().Put(u.Name, u)
		}
		return nil
	})
}

func (us users) Locked(ctx context.Context) ([]string, error) {
	out := []string{}
	err := us.s.Do(ctx, func() error {
		for _, u := range us.s.usersT().All(nil, compareByKey(userName)) {
			if u.Status == model.UserLocked {
				out = append(out, u.Name)
			}
		}
		return nil
	})
	return out, err
}

// --- Groups ----------------------------------------------------------------

type groups struct{ s *Store }

func groupName(g model.Group) string { return g.Name }

func (gs groups) Create(ctx context.Context, g model.Group) error {
	return gs.s.Do(ctx, func() error {
		if gs.s.groupsT().Has(g.Name) {
			return memtx.Conflict("group", g.Name)
		}
		return gs.write(g)
	})
}

// write stores g and replaces its members, after checking they exist.
func (gs groups) write(g model.Group) error {
	members := sortedUnique(g.Members)
	if err := requireKeys(gs.s.usersT(), "user", members); err != nil {
		return err
	}
	g.Members = nil
	gs.s.groupsT().Put(g.Name, g)
	gs.s.membersT().DeleteWhere(func(m membership, _ struct{}) bool { return m.Group == g.Name })
	for _, u := range members {
		gs.s.membersT().Put(membership{g.Name, u}, struct{}{})
	}
	return nil
}

func (gs groups) Get(ctx context.Context, name string) (model.Group, error) {
	var out model.Group
	err := gs.s.Do(ctx, func() error {
		g, ok := gs.s.groupsT().Get(name)
		if !ok {
			return memtx.NotFound("group", name)
		}
		g.Members = gs.s.membersOf(name)
		out = g
		return nil
	})
	return out, err
}

func (gs groups) Update(ctx context.Context, g model.Group) error {
	return gs.s.Do(ctx, func() error {
		if !gs.s.groupsT().Has(g.Name) {
			return memtx.NotFound("group", g.Name)
		}
		return gs.write(g)
	})
}

func (gs groups) Delete(ctx context.Context, name string) error {
	return gs.s.Do(ctx, func() error {
		if !gs.s.groupsT().Del(name) {
			return memtx.NotFound("group", name)
		}
		gs.s.membersT().DeleteWhere(func(m membership, _ struct{}) bool { return m.Group == name })
		return nil
	})
}

func (gs groups) List(ctx context.Context, p kernel.Page) ([]model.Group, string, error) {
	var (
		out  []model.Group
		next string
	)
	err := gs.s.Do(ctx, func() error {
		rows := gs.s.groupsT().All(nil, compareByKey(groupName))
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(g model.Group) []any { return []any{g.Name} },
		)
		for i := range out {
			out[i].Members = gs.s.membersOf(out[i].Name)
		}
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (gs groups) AddMembers(ctx context.Context, group string, users ...string) error {
	return gs.s.Do(ctx, func() error {
		if !gs.s.groupsT().Has(group) {
			return memtx.NotFound("group", group)
		}
		users = sortedUnique(users)
		if err := requireKeys(gs.s.usersT(), "user", users); err != nil {
			return err
		}
		for _, u := range users {
			gs.s.membersT().Put(membership{group, u}, struct{}{})
		}
		return nil
	})
}

func (gs groups) RemoveMembers(ctx context.Context, group string, users ...string) error {
	return gs.s.Do(ctx, func() error {
		if !gs.s.groupsT().Has(group) {
			return memtx.NotFound("group", group)
		}
		for _, u := range users {
			gs.s.membersT().Del(membership{group, u})
		}
		return nil
	})
}

func (gs groups) AutoJoin(ctx context.Context) ([]string, error) {
	var out []string
	err := gs.s.Do(ctx, func() error {
		for _, g := range gs.s.groupsT().All(func(_ string, g model.Group) bool { return g.AutoJoin }, compareByKey(groupName)) {
			out = append(out, g.Name)
		}
		return nil
	})
	return out, err
}

// --- Permissions -----------------------------------------------------------

type permissions struct{ s *Store }

func (s *Store) permissionsT() *memtx.Table[string, model.Permission] {
	return memtx.TableOf[string, model.Permission](s.Store, "permissions")
}

func permissionName(p model.Permission) string { return p.Name }

func (ps permissions) Create(ctx context.Context, p model.Permission) error {
	return ps.s.Do(ctx, func() error {
		if ps.s.permissionsT().Has(p.Name) {
			return memtx.Conflict("permission", p.Name)
		}
		ps.s.permissionsT().Put(p.Name, p)
		return nil
	})
}

func (ps permissions) Get(ctx context.Context, name string) (model.Permission, error) {
	var out model.Permission
	err := ps.s.Do(ctx, func() error {
		var ok bool
		if out, ok = ps.s.permissionsT().Get(name); !ok {
			return memtx.NotFound("permission", name)
		}
		return nil
	})
	return out, err
}

func (ps permissions) Update(ctx context.Context, p model.Permission) error {
	return ps.s.Do(ctx, func() error {
		if !ps.s.permissionsT().Has(p.Name) {
			return memtx.NotFound("permission", p.Name)
		}
		ps.s.permissionsT().Put(p.Name, p)
		return nil
	})
}

func (ps permissions) Delete(ctx context.Context, name string) error {
	return ps.s.Do(ctx, func() error {
		if !ps.s.permissionsT().Del(name) {
			return memtx.NotFound("permission", name)
		}
		return nil
	})
}

func (ps permissions) List(ctx context.Context, p kernel.Page) ([]model.Permission, string, error) {
	var (
		out  []model.Permission
		next string
	)
	err := ps.s.Do(ctx, func() error {
		rows := ps.s.permissionsT().All(nil, compareByKey(permissionName))
		var err error
		out, next, err = memtx.Paginate(
			rows,
			p,
			false,
			func(v model.Permission) []any { return []any{v.Name} },
		)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

// where lists the permissions matching keep, by name.
func (ps permissions) where(
	ctx context.Context,
	keep func(model.Permission) bool,
) ([]model.Permission, error) {
	var out []model.Permission
	err := ps.s.Do(ctx, func() error {
		out = ps.s.permissionsT().
			All(func(_ string, p model.Permission) bool { return keep(p) }, compareByKey(permissionName))
		return nil
	})
	return out, err
}

func (ps permissions) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]model.Permission, error) {
	return ps.where(ctx, func(p model.Permission) bool {
		for _, r := range p.Resources {
			if r.Actions.Users[user] != 0 {
				return true
			}
			for _, g := range groups {
				if r.Actions.Groups[g] != 0 {
					return true
				}
			}
		}
		return false
	})
}

func (ps permissions) WithTarget(ctx context.Context, target string) ([]model.Permission, error) {
	return ps.where(ctx, func(p model.Permission) bool {
		for _, r := range p.Resources {
			if _, ok := r.Targets[target]; ok {
				return true
			}
		}
		return false
	})
}
