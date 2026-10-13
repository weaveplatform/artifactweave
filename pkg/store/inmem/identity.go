package inmem

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Users implements store.Store.
func (s *Store) Users() store.UserStore { return users{s} }

// Groups implements store.Store.
func (s *Store) Groups() store.GroupStore { return groups{s} }

// Permissions implements store.Store.
func (s *Store) Permissions() store.PermissionStore { return permissions{s} }

// membership is a group_members row.
type membership struct{ Group, User string }

func (s *Store) usersT() *table[string, types.User] { return tableOf[string, types.User](s, "users") }

func (s *Store) groupsT() *table[string, types.Group] {
	return tableOf[string, types.Group](s, "user_groups")
}

func (s *Store) membersT() *table[membership, struct{}] {
	return tableOf[membership, struct{}](s, "group_members")
}

// sortedUnique returns names sorted and without repeats.
func sortedUnique(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// requireKeys returns ErrNotFound for the first of names t has no row for.
func requireKeys[V any](t *table[string, V], what string, names []string) error {
	for _, n := range names {
		if !t.has(n) {
			return notFound(what, n)
		}
	}
	return nil
}

// groupsOf returns a user's groups, sorted.
func (s *Store) groupsOf(user string) []string {
	var out []string
	for m := range s.membersT().rows {
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
	for m := range s.membersT().rows {
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

func userName(u types.User) string { return u.Name }

func (us users) Create(ctx context.Context, u types.User) error {
	return us.s.do(ctx, func() error {
		if us.s.usersT().has(u.Name) {
			return conflict("user", u.Name)
		}
		return us.write(u)
	})
}

// write stores u and replaces its memberships, after checking its groups.
func (us users) write(u types.User) error {
	groups := sortedUnique(u.Groups)
	if err := requireKeys(us.s.groupsT(), "group", groups); err != nil {
		return err
	}
	u.Groups = nil
	us.s.usersT().put(u.Name, u)
	us.s.membersT().deleteWhere(func(m membership, _ struct{}) bool { return m.User == u.Name })
	for _, g := range groups {
		us.s.membersT().put(membership{g, u.Name}, struct{}{})
	}
	return nil
}

func (us users) Get(ctx context.Context, name string) (types.User, error) {
	var out types.User
	err := us.s.do(ctx, func() error {
		u, ok := us.s.usersT().get(name)
		if !ok {
			return notFound("user", name)
		}
		u.Groups = us.s.groupsOf(name)
		out = u
		return nil
	})
	return out, err
}

func (us users) Update(ctx context.Context, u types.User) error {
	return us.s.do(ctx, func() error {
		if !us.s.usersT().has(u.Name) {
			return notFound("user", u.Name)
		}
		return us.write(u)
	})
}

func (us users) Delete(ctx context.Context, name string) error {
	return us.s.do(ctx, func() error {
		if !us.s.usersT().del(name) {
			return notFound("user", name)
		}
		us.s.membersT().deleteWhere(func(m membership, _ struct{}) bool { return m.User == name })
		return nil
	})
}

func (us users) List(ctx context.Context, p store.Page) ([]types.User, string, error) {
	var (
		out  []types.User
		next string
	)
	err := us.s.do(ctx, func() error {
		rows := us.s.usersT().all(nil, compareByKey(userName))
		var err error
		out, next, err = paginate(rows, p, false, func(u types.User) []any { return []any{u.Name} })
		for i := range out {
			out[i].Groups = us.s.groupsOf(out[i].Name)
		}
		return err
	})
	return out, next, err
}

func (us users) RecordLogin(ctx context.Context, name string, at time.Time) error {
	return us.s.do(ctx, func() error {
		u, ok := us.s.usersT().get(name)
		if !ok {
			return notFound("user", name)
		}
		u.LastLoggedIn = at
		u.FailedLogins = 0
		us.s.usersT().put(name, u)
		return nil
	})
}

func (us users) RecordFailedLogin(ctx context.Context, name string, lockAt int) (bool, error) {
	var locked bool
	err := us.s.do(ctx, func() error {
		u, ok := us.s.usersT().get(name)
		if !ok {
			return notFound("user", name)
		}
		u.FailedLogins++
		enabled := u.Status == "" || u.Status == types.UserEnabled
		if lockAt > 0 && u.FailedLogins >= lockAt && enabled {
			u.Status = types.UserLocked
		}
		locked = u.Status == types.UserLocked
		us.s.usersT().put(name, u)
		return nil
	})
	return locked, err
}

func (us users) Unlock(ctx context.Context, names ...string) error {
	return us.s.do(ctx, func() error {
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		for _, u := range us.s.usersT().all(nil, compareByKey(userName)) {
			switch {
			case len(names) > 0 && !want[u.Name]:
				continue
			case len(names) == 0 && u.Status != types.UserLocked && u.FailedLogins == 0:
				continue
			}
			if u.Status == types.UserLocked {
				u.Status = types.UserEnabled
			}
			u.FailedLogins = 0
			us.s.usersT().put(u.Name, u)
		}
		return nil
	})
}

func (us users) Locked(ctx context.Context) ([]string, error) {
	out := []string{}
	err := us.s.do(ctx, func() error {
		for _, u := range us.s.usersT().all(nil, compareByKey(userName)) {
			if u.Status == types.UserLocked {
				out = append(out, u.Name)
			}
		}
		return nil
	})
	return out, err
}

// --- Groups ----------------------------------------------------------------

type groups struct{ s *Store }

func groupName(g types.Group) string { return g.Name }

func (gs groups) Create(ctx context.Context, g types.Group) error {
	return gs.s.do(ctx, func() error {
		if gs.s.groupsT().has(g.Name) {
			return conflict("group", g.Name)
		}
		return gs.write(g)
	})
}

// write stores g and replaces its members, after checking they exist.
func (gs groups) write(g types.Group) error {
	members := sortedUnique(g.Members)
	if err := requireKeys(gs.s.usersT(), "user", members); err != nil {
		return err
	}
	g.Members = nil
	gs.s.groupsT().put(g.Name, g)
	gs.s.membersT().deleteWhere(func(m membership, _ struct{}) bool { return m.Group == g.Name })
	for _, u := range members {
		gs.s.membersT().put(membership{g.Name, u}, struct{}{})
	}
	return nil
}

func (gs groups) Get(ctx context.Context, name string) (types.Group, error) {
	var out types.Group
	err := gs.s.do(ctx, func() error {
		g, ok := gs.s.groupsT().get(name)
		if !ok {
			return notFound("group", name)
		}
		g.Members = gs.s.membersOf(name)
		out = g
		return nil
	})
	return out, err
}

func (gs groups) Update(ctx context.Context, g types.Group) error {
	return gs.s.do(ctx, func() error {
		if !gs.s.groupsT().has(g.Name) {
			return notFound("group", g.Name)
		}
		return gs.write(g)
	})
}

func (gs groups) Delete(ctx context.Context, name string) error {
	return gs.s.do(ctx, func() error {
		if !gs.s.groupsT().del(name) {
			return notFound("group", name)
		}
		gs.s.membersT().deleteWhere(func(m membership, _ struct{}) bool { return m.Group == name })
		return nil
	})
}

func (gs groups) List(ctx context.Context, p store.Page) ([]types.Group, string, error) {
	var (
		out  []types.Group
		next string
	)
	err := gs.s.do(ctx, func() error {
		rows := gs.s.groupsT().all(nil, compareByKey(groupName))
		var err error
		out, next, err = paginate(rows, p, false, func(g types.Group) []any { return []any{g.Name} })
		for i := range out {
			out[i].Members = gs.s.membersOf(out[i].Name)
		}
		return err
	})
	return out, next, err
}

func (gs groups) AddMembers(ctx context.Context, group string, users ...string) error {
	return gs.s.do(ctx, func() error {
		if !gs.s.groupsT().has(group) {
			return notFound("group", group)
		}
		users = sortedUnique(users)
		if err := requireKeys(gs.s.usersT(), "user", users); err != nil {
			return err
		}
		for _, u := range users {
			gs.s.membersT().put(membership{group, u}, struct{}{})
		}
		return nil
	})
}

func (gs groups) RemoveMembers(ctx context.Context, group string, users ...string) error {
	return gs.s.do(ctx, func() error {
		if !gs.s.groupsT().has(group) {
			return notFound("group", group)
		}
		for _, u := range users {
			gs.s.membersT().del(membership{group, u})
		}
		return nil
	})
}

func (gs groups) AutoJoin(ctx context.Context) ([]string, error) {
	var out []string
	err := gs.s.do(ctx, func() error {
		for _, g := range gs.s.groupsT().all(func(_ string, g types.Group) bool { return g.AutoJoin }, compareByKey(groupName)) {
			out = append(out, g.Name)
		}
		return nil
	})
	return out, err
}

// --- Permissions -----------------------------------------------------------

type permissions struct{ s *Store }

func (s *Store) permissionsT() *table[string, types.Permission] {
	return tableOf[string, types.Permission](s, "permissions")
}

func permissionName(p types.Permission) string { return p.Name }

func (ps permissions) Create(ctx context.Context, p types.Permission) error {
	return ps.s.do(ctx, func() error {
		if ps.s.permissionsT().has(p.Name) {
			return conflict("permission", p.Name)
		}
		ps.s.permissionsT().put(p.Name, p)
		return nil
	})
}

func (ps permissions) Get(ctx context.Context, name string) (types.Permission, error) {
	var out types.Permission
	err := ps.s.do(ctx, func() error {
		var ok bool
		if out, ok = ps.s.permissionsT().get(name); !ok {
			return notFound("permission", name)
		}
		return nil
	})
	return out, err
}

func (ps permissions) Update(ctx context.Context, p types.Permission) error {
	return ps.s.do(ctx, func() error {
		if !ps.s.permissionsT().has(p.Name) {
			return notFound("permission", p.Name)
		}
		ps.s.permissionsT().put(p.Name, p)
		return nil
	})
}

func (ps permissions) Delete(ctx context.Context, name string) error {
	return ps.s.do(ctx, func() error {
		if !ps.s.permissionsT().del(name) {
			return notFound("permission", name)
		}
		return nil
	})
}

func (ps permissions) List(ctx context.Context, p store.Page) ([]types.Permission, string, error) {
	var (
		out  []types.Permission
		next string
	)
	err := ps.s.do(ctx, func() error {
		rows := ps.s.permissionsT().all(nil, compareByKey(permissionName))
		var err error
		out, next, err = paginate(rows, p, false, func(v types.Permission) []any { return []any{v.Name} })
		return err
	})
	return out, next, err
}

// where lists the permissions matching keep, by name.
func (ps permissions) where(ctx context.Context, keep func(types.Permission) bool) ([]types.Permission, error) {
	var out []types.Permission
	err := ps.s.do(ctx, func() error {
		out = ps.s.permissionsT().all(func(_ string, p types.Permission) bool { return keep(p) }, compareByKey(permissionName))
		return nil
	})
	return out, err
}

func (ps permissions) ForPrincipal(ctx context.Context, user string, groups []string) ([]types.Permission, error) {
	return ps.where(ctx, func(p types.Permission) bool {
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

func (ps permissions) WithTarget(ctx context.Context, target string) ([]types.Permission, error) {
	return ps.where(ctx, func(p types.Permission) bool {
		for _, r := range p.Resources {
			if _, ok := r.Targets[target]; ok {
				return true
			}
		}
		return false
	})
}
