package permission

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PrincipalsOf returns the principals a filtered listing asks about, as
// Granting gathers them: the named users' stored names and, with
// inherited, their groups, and the named groups. ErrNotFound names the
// first user, then the first group, that does not exist, as Granting's
// does.
func (s Service) PrincipalsOf(
	ctx context.Context,
	users, groups []string,
	inherited bool,
) (Principals, error) {
	var pr Principals
	for _, name := range users {
		u, err := s.Store.Users().Get(ctx, model.FoldUsername(name))
		if errors.Is(err, kernel.ErrNotFound) {
			return Principals{}, fmt.Errorf("%w: User '%s' does not exist", ErrNotFound, name)
		}
		if err != nil {
			return Principals{}, fmt.Errorf("permission: user %s: %w", name, err)
		}
		pr.Users = append(pr.Users, u.Name)
		if inherited {
			pr.Groups = append(pr.Groups, u.Groups...)
		}
	}
	for _, name := range groups {
		if _, err := s.Store.Groups().Get(ctx, name); errors.Is(err, kernel.ErrNotFound) {
			return Principals{}, fmt.Errorf("%w: Group '%s' does not exist", ErrNotFound, name)
		} else if err != nil {
			return Principals{}, fmt.Errorf("permission: group %s: %w", name, err)
		}
		pr.Groups = append(pr.Groups, name)
	}
	slices.Sort(pr.Users)
	pr.Users = slices.Compact(pr.Users)
	slices.Sort(pr.Groups)
	pr.Groups = slices.Compact(pr.Groups)
	return pr, nil
}

// Page returns up to limit permissions named after after, ordered by name,
// and whether more follow, read from the store a page at a time rather than
// collected: every permission, or with pr only those granting some action
// to one of its users or groups, which is the union Granting collects.
func (s Service) Page(
	ctx context.Context,
	after string,
	limit int,
	pr *Principals,
) ([]model.Permission, bool, error) {
	q := port.PermissionQuery{After: after, Limit: limit}
	if pr != nil {
		q.Granting, q.Users, q.Groups = true, pr.Users, pr.Groups
	}
	ps, more, err := s.Store.Permissions().Page(ctx, q)
	if err != nil {
		return nil, false, fmt.Errorf("permission: page: %w", err)
	}
	return ps, more, nil
}
