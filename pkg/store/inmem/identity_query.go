package inmem

import (
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (us users) ListSummaries(ctx context.Context, f store.UserFilter, p store.Page) (
	[]store.UserSummary, string, error,
) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err
	}
	after, err := p.Key(1)
	if err != nil {
		return nil, "", err
	}
	var out []store.UserSummary
	compare := func(a, b store.UserSummary) int {
		return identityOrder(a.Name, b.Name, f.Descending)
	}
	err = us.s.do(ctx, func() error {
		for _, u := range us.s.usersT().rows {
			if !identityName(u.Name, f.IdentityFilter, after) {
				continue
			}
			status := u.Status
			if status == "" {
				status = types.UserEnabled
			}
			if f.Status != "" && f.Status != status {
				continue
			}
			var memberships []string
			if f.OnlyAdmins || f.Resource != nil {
				memberships = us.s.groupsOf(u.Name)
			}
			admin := u.Admin
			for _, name := range memberships {
				g, _ := us.s.groupsT().get(name)
				admin = admin || g.AdminPrivileges
			}
			if f.OnlyAdmins && !admin ||
				!us.s.identityGranted(u.Name, memberships, f.Resource, false) {
				continue
			}
			v := store.UserSummary{Name: u.Name, Realm: u.Realm, Status: status}
			i, _ := slices.BinarySearchFunc(out, v, compare)
			out = slices.Insert(out, i, v)
			if len(out) > p.Limit+1 {
				out = out[:p.Limit+1]
			}
		}
		return nil
	})
	out, next := store.Trim(
		out,
		p.Limit,
		func(u store.UserSummary) []string { return []string{u.Name} },
	)
	return out, next, err
}

func (gs groups) ListNames(
	ctx context.Context,
	f store.IdentityFilter,
	p store.Page,
) ([]string, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err
	}
	after, err := p.Key(1)
	if err != nil {
		return nil, "", err
	}
	var out []string
	compare := func(a, b string) int { return identityOrder(a, b, f.Descending) }
	err = gs.s.do(ctx, func() error {
		for name := range gs.s.groupsT().rows {
			if identityName(name, f, after) && gs.s.identityGranted(name, nil, f.Resource, true) {
				i, _ := slices.BinarySearchFunc(out, name, compare)
				out = slices.Insert(out, i, name)
				if len(out) > p.Limit+1 {
					out = out[:p.Limit+1]
				}
			}
		}
		return nil
	})
	out, next := store.Trim(out, p.Limit, func(name string) []string { return []string{name} })
	return out, next, err
}

func identityOrder(a, b string, descending bool) int {
	if descending {
		return strings.Compare(b, a)
	}
	return strings.Compare(a, b)
}

func identityName(name string, f store.IdentityFilter, after []string) bool {
	if !strings.Contains(strings.ToLower(name), strings.ToLower(f.NameContains)) {
		return false
	}
	return len(after) == 0 || f.Descending && name < after[0] || !f.Descending && name > after[0]
}

func (s *Store) identityGranted(
	name string,
	memberships []string,
	target *store.IdentityResource,
	group bool,
) bool {
	if target == nil {
		return true
	}
	for _, p := range s.permissionsT().rows {
		r := p.Resources[target.Type]
		if _, ok := r.Targets[target.Name]; !ok {
			continue
		}
		if group {
			if r.Actions.Groups[name] != 0 {
				return true
			}
			continue
		}
		if r.Actions.Users[name] != 0 {
			return true
		}
		for _, g := range memberships {
			if r.Actions.Groups[g] != 0 {
				return true
			}
		}
	}
	return false
}
