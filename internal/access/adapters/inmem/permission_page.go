package inmem

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
)

// Page is the SQL store's Page over the in-memory table: the same name
// order, grant filter and one-past-the-page read. The table is a map, so
// the matches are gathered and sorted first, which the SQL store's primary
// key does instead.
func (ps permissions) Page(
	ctx context.Context,
	q port.PermissionQuery,
) ([]model.Permission, bool, error) {
	var out []model.Permission
	err := ps.s.Do(ctx, func() error {
		out = ps.s.permissionsT().All(func(_ string, p model.Permission) bool {
			return (q.After == "" || p.Name > q.After) && (!q.Granting || grantsAny(p, q))
		}, compareByKey(permissionName))
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > q.Limit {
		return out[:q.Limit], true, nil
	}
	return out, false, nil
}

// grantsAny reports whether p grants some action to one of q's users or
// groups.
func grantsAny(p model.Permission, q port.PermissionQuery) bool {
	for _, r := range p.Resources {
		for name, a := range r.Actions.Users {
			if a != 0 && slices.Contains(q.Users, name) {
				return true
			}
		}
		for name, a := range r.Actions.Groups {
			if a != 0 && slices.Contains(q.Groups, name) {
				return true
			}
		}
	}
	return false
}
