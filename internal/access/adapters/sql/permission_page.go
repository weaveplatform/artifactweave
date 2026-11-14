package sql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Page reads one page of permissions by name, LIMIT one past it: a seek on
// the primary key after q.After, with the grant filter as a subquery on
// permission_grants' principal index, as ForPrincipal filters.
func (ps permissions) Page(
	ctx context.Context,
	q port.PermissionQuery,
) ([]model.Permission, bool, error) {
	query, args := permissionPageQuery(q)
	var out []model.Permission
	err := ps.s.Rows(ctx, "page permissions", query, args, func(r *sql.Rows) error {
		p, err := scanPermission(r)
		out = append(out, p)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > q.Limit {
		return out[:q.Limit], true, nil
	}
	return out, false, nil
}

// permissionPageQuery is the statement Page sends for q, with its
// arguments.
func permissionPageQuery(q port.PermissionQuery) (string, []any) {
	where, args := permissionPageWhere(q)
	return "SELECT " + permissionCols + " FROM permissions WHERE " + where +
		dialect.OrderBy(nameKeyset, false) + " LIMIT ?", append(args, q.Limit+1)
}

func permissionPageWhere(q port.PermissionQuery) (string, []any) {
	conds := []string{"1 = 1"}
	var args []any
	if q.After != "" {
		conds = append(conds, "name > ?")
		args = append(args, q.After)
	}
	if q.Granting {
		var who []string
		for _, p := range []struct {
			typ   model.PrincipalType
			names []string
		}{{model.PrincipalUser, q.Users}, {model.PrincipalGroup, q.Groups}} {
			if len(p.names) == 0 {
				continue
			}
			who = append(who, "(principal_type = ? AND principal_name IN ("+
				dialect.Placeholders(len(p.names))+"))")
			args = append(args, string(p.typ))
			for _, n := range p.names {
				args = append(args, n)
			}
		}
		if who == nil {
			who = []string{"1 = 0"}
		}
		conds = append(conds, "name IN (SELECT permission_name FROM permission_grants "+
			"WHERE actions <> 0 AND ("+strings.Join(who, " OR ")+"))")
	}
	return strings.Join(conds, " AND "), args
}
