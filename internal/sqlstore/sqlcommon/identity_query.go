package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Fold with Go on write: database LOWER follows engine/locale-specific rules,
// while the existing Access name filter uses strings.ToLower.
func (s *Store) writeIdentitySearch(ctx context.Context, table, name string) error {
	_, err := s.exec(
		ctx,
		"index identity name",
		"UPDATE "+table+" SET search_name = ? WHERE name = ?",
		strings.ToLower(name),
		name,
	)
	return err
}

// Backfill one bounded page per transaction. A failed upgrade can resume from
// NULL rows; the server does not begin serving before this completes.
func (s *Store) backfillIdentitySearch(ctx context.Context) error {
	for _, table := range []string{"users", "user_groups"} {
		for {
			var names []string
			err := s.Tx(ctx, func(ctx context.Context) error {
				err := s.rows(ctx, "read identity search backfill",
					"SELECT name FROM "+table+" WHERE search_name IS NULL ORDER BY name LIMIT ?",
					[]any{store.MaxLimit}, func(r *sql.Rows) error {
						var name string
						if err := r.Scan(&name); err != nil {
							return err //nolint:wrapcheck // rows supplies context
						}
						names = append(names, name)
						return nil
					})
				if err != nil {
					return err
				}
				for _, name := range names {
					if err := s.writeIdentitySearch(ctx, table, name); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			if len(names) < store.MaxLimit {
				break
			}
		}
	}
	return nil
}

func (s *Store) identityWhere(
	f store.IdentityFilter,
	p store.Page,
	group bool,
) (string, []any, error) {
	where, args, err := keyset(p, []string{"i.name"}, nil, f.Descending)
	if err != nil {
		return "", nil, err
	}
	if f.NameContains != "" {
		contains := "instr(i.search_name, ?) > 0"
		if s.d.Dollar {
			contains = "position(? in i.search_name) > 0"
		}
		where += " AND " + contains
		args = append(args, strings.ToLower(f.NameContains))
	}
	if f.Resource != nil {
		principal := "(g.principal_type = 'user' AND g.principal_name = i.name) OR " +
			"(g.principal_type = 'group' AND EXISTS (SELECT 1 FROM group_members m " +
			"WHERE m.user_name = i.name AND m.group_name = g.principal_name))"
		if group {
			principal = "g.principal_type = 'group' AND g.principal_name = i.name"
		}
		where += " AND EXISTS (SELECT 1 FROM permission_grants g JOIN permission_targets t " +
			"ON t.permission_name = g.permission_name AND t.resource_type = g.resource_type " +
			"WHERE t.resource_type = ? AND t.target = ? AND g.actions <> 0 AND (" + principal + "))"
		args = append(args, string(f.Resource.Type), f.Resource.Name)
	}
	return where, args, nil
}

func (us users) ListSummaries(ctx context.Context, f store.UserFilter, p store.Page) (
	[]store.UserSummary, string, error,
) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("page user summaries: %w", err)
	}
	where, args, err := us.s.identityWhere(f.IdentityFilter, p, false)
	if err != nil {
		return nil, "", err
	}
	const status = "COALESCE(NULLIF(i.status, ''), 'enabled')"
	if f.Status != "" {
		where += " AND " + status + " = ?"
		args = append(args, string(f.Status))
	}
	if f.OnlyAdmins {
		where += " AND (i.admin = TRUE OR EXISTS (SELECT 1 FROM group_members m " +
			"JOIN user_groups g ON g.name = m.group_name " +
			"WHERE m.user_name = i.name AND g.admin_privileges = TRUE))"
	}
	query := "SELECT i.name, i.realm, " + status + " FROM users i WHERE " + where +
		orderBy([]string{"i.name"}, f.Descending) + " LIMIT ?"
	args = append(args, p.Limit+1)
	out := make([]store.UserSummary, 0, p.Limit+1)
	err = us.s.rows(ctx, "page user summaries", query, args, func(r *sql.Rows) error {
		var u store.UserSummary
		if err := r.Scan(&u.Name, &u.Realm, &u.Status); err != nil {
			return err //nolint:wrapcheck // rows supplies context
		}
		out = append(out, u)
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
		return nil, "", fmt.Errorf("page group names: %w", err)
	}
	where, args, err := gs.s.identityWhere(f, p, true)
	if err != nil {
		return nil, "", err
	}
	query := "SELECT i.name FROM user_groups i WHERE " + where +
		orderBy([]string{"i.name"}, f.Descending) + " LIMIT ?"
	out := make([]string, 0, p.Limit+1)
	err = gs.s.rows(
		ctx,
		"page group names",
		query,
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			var name string
			if err := r.Scan(&name); err != nil {
				return err //nolint:wrapcheck // rows supplies context
			}
			out = append(out, name)
			return nil
		},
	)
	out, next := store.Trim(out, p.Limit, func(name string) []string { return []string{name} })
	return out, next, err
}
