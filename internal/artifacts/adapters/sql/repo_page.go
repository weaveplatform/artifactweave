package sql

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Page answers one window of the repository list in two statements: a
// COUNT over the same WHERE when l asks for one, then the window itself,
// ordered by the primary key and read with LIMIT one past the window. The
// count and the window are separate statements, so a repository created or
// removed between them can make Total disagree with the window by one; the
// list promises no snapshot (decision 0043, "Access identity pagination").
// Only the window's documents are decoded, for their descriptions, and no
// secret is read.
func (r repos) Page(ctx context.Context, l port.RepoListing) (port.RepoPage, error) {
	count, window, countArgs, windowArgs := repoPageQueries(l)
	var out port.RepoPage
	if l.Count {
		err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind(count), countArgs...).
			Scan(&out.Total)
		if err != nil {
			return port.RepoPage{}, r.s.Wrap("count repositories", err)
		}
		if l.Offset >= out.Total {
			// Past the end: the window is empty and nothing follows it.
			return out, nil
		}
	}
	err := r.s.Rows(ctx, "page repositories", window, windowArgs, func(rs *sql.Rows) error {
		var s port.RepoSummary
		var doc string
		if err := rs.Scan(&s.Key, &s.Class, &s.PackageType, &doc); err != nil {
			return err //nolint:wrapcheck // wrapped by Rows
		}
		c, err := decodeRepo(doc)
		if err != nil {
			return err
		}
		s.Description = c.Description
		out.Repos = append(out.Repos, s)
		return nil
	})
	if err != nil {
		return port.RepoPage{}, err
	}
	if l.Limit > 0 && len(out.Repos) > l.Limit {
		out.Repos, out.More = out.Repos[:l.Limit], true
	}
	return out, nil
}

// repoPageQueries returns Page's two statements for l with their
// arguments: the count, and the window, which reads one row past l.Limit.
func repoPageQueries(l port.RepoListing) (count, window string, countArgs, windowArgs []any) {
	where, args := repoListingWhere(l)
	count = "SELECT COUNT(*) FROM repositories r WHERE " + where
	window = "SELECT r.key, r.rclass, r.package_type, r.doc FROM repositories r WHERE " +
		where + " ORDER BY r.key"
	windowArgs = args
	if l.Limit > 0 {
		window += " LIMIT ? OFFSET ?"
		windowArgs = append(slices.Clip(args), l.Limit+1, l.Offset)
	}
	return count, window, args, windowArgs
}

// repoListingWhere is l's filter as a WHERE condition over repositories r.
// Each visibility set is one conjunct; a set naming nothing matches no row.
func repoListingWhere(l port.RepoListing) (string, []any) {
	conds := []string{"1 = 1"}
	var args []any
	if l.Class != "" {
		conds = append(conds, "r.rclass = ?")
		args = append(args, string(l.Class))
	}
	if l.ProjectKey != "" {
		conds = append(conds, "r.project_key = ?")
		args = append(args, l.ProjectKey)
	}
	if l.After != "" {
		// The primary key seeks past it, in the order the window reads.
		conds = append(conds, "r.key > ?")
		args = append(args, l.After)
	}
	if l.PackageTypes != nil {
		conds = append(conds, inList("r.package_type", len(l.PackageTypes)))
		for _, t := range l.PackageTypes {
			args = append(args, string(t))
		}
	}
	for _, s := range l.Visible {
		if s.Every {
			continue
		}
		var or []string
		if len(s.Classes) > 0 {
			or = append(or, inList("r.rclass", len(s.Classes)))
			for _, c := range s.Classes {
				args = append(args, string(c))
			}
		}
		if len(s.Keys) > 0 {
			or = append(or, inList("r.key", len(s.Keys)))
			for _, k := range s.Keys {
				args = append(args, k)
			}
		}
		if or == nil {
			or = []string{"1 = 0"}
		}
		conds = append(conds, "("+strings.Join(or, " OR ")+")")
	}
	return strings.Join(conds, " AND "), args
}

// inList is "col IN (?, ...)" for n values, or a condition no row meets
// for none, which an empty IN list is not valid SQL for.
func inList(col string, n int) string {
	if n == 0 {
		return "1 = 0"
	}
	return col + " IN (" + dialect.Placeholders(n) + ")"
}
