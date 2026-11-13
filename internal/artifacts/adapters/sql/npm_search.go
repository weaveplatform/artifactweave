package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// SearchNpm returns an npm search's candidates (port.NpmSearch).
func (p packages) SearchNpm(
	ctx context.Context,
	q port.NpmSearch,
	pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	return p.search(ctx, "search npm packages", q.RepoKey, q.Terms, "", pg)
}

// search returns a text search's candidates in repoKey whose names start
// with prefix, one page of them by name, each with its versions. The page
// is one seek of packages_name, whose order needs no sort, each package's
// versions tested through an index on package_id; then the page's
// versions are read in one query, through one too. A package removed
// between the two queries has no versions left and is dropped.
func (p packages) search(
	ctx context.Context,
	op, repoKey string,
	terms []string,
	prefix string,
	pg kernel.Page,
) ([]port.SearchCandidate, string, error) {
	pg, err := pg.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	if strings.ContainsRune(prefix, 0) {
		// No name holds a NUL byte, which PostgreSQL cannot take.
		return nil, "", nil
	}
	q, args, err := searchQuery(p.s.Dialect(), repoKey, terms, prefix, pg)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", op, err)
	}
	var found []port.SearchCandidate
	err = p.s.Rows(ctx, op, q, args, func(rs *sql.Rows) error {
		v, err := scanPackage(rs)
		found = append(found, port.SearchCandidate{Package: v})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(found, pg.Limit,
		func(c port.SearchCandidate) []string { return []string{c.Package.Name} })
	if len(page) == 0 {
		return nil, next, nil
	}
	ids := make([]any, len(page))
	at := make(map[int64]int, len(page))
	for i, c := range page {
		ids[i], at[c.Package.ID] = c.Package.ID, i
	}
	versions, err := p.versions(ctx, op, searchVersionsQuery(len(ids)), ids...)
	if err != nil {
		return nil, "", err
	}
	for _, v := range versions {
		i := at[v.PackageID]
		page[i].Versions = append(page[i].Versions, v)
	}
	out := page[:0]
	for _, c := range page {
		if len(c.Versions) > 0 {
			out = append(out, c)
		}
	}
	return out, next, nil
}

// searchQuery is the query of a page of a search's candidates (pg
// normalised), as port's npm_search.go defines them, with its arguments.
// A run is looked for with instr (SQLite) or strpos (PostgreSQL), which
// compare bytes, so a run's % or _ means itself. The fold is written as
// SearchFold is: the two replacements, then lower(), which PostgreSQL
// is made to apply to ASCII alone, as SQLite does, by the "C" collation,
// whatever collation the database has (a Turkish one would make "I" a
// dotless i).
func searchQuery(
	d dialect.Dialect,
	repoKey string,
	terms []string,
	prefix string,
	pg kernel.Page,
) (string, []any, error) {
	keyset, after, err := dialect.Keyset(pg, []string{"p.name"}, nil, false)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // the caller wraps it
	}
	contains, collate := "instr(%s, ?) > 0", ""
	if d.Name == "postgres" {
		contains, collate = "strpos(%s, ?) > 0", ` COLLATE "C"`
	}
	fold := func(col string) string {
		for _, pair := range port.SearchFoldPairs {
			col = "replace(" + col + ", '" + pair[0] + "', '" + pair[1] + "')"
		}
		return "lower(" + col + collate + ")"
	}
	name, meta := fold("p.name"), fold("v.metadata")
	q := "SELECT p.id, p.repo_key, p.package_type, p.name, p.created_at FROM packages p" +
		" WHERE p.repo_key = ? AND " + keyset
	args := append([]any{repoKey}, after...)
	if prefix != "" {
		// The names that start with prefix are those from it up to the
		// first that sorts after them all, so the seek stays in them.
		q += " AND p.name >= ?"
		args = append(args, prefix)
		if end, ok := port.SearchPrefixEnd(prefix); ok {
			q += " AND p.name < ?"
			args = append(args, end)
		}
	}
	q += " AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id"
	if runs := port.SearchRuns(terms); len(runs) > 0 {
		var all []string
		args = append(args, port.SearchEscape)
		for _, term := range runs {
			inName := make([]string, len(term))
			inMeta := make([]string, len(term))
			for i := range term {
				inName[i], inMeta[i] = fmt.Sprintf(contains, name), fmt.Sprintf(contains, meta)
			}
			all = append(all, "(("+strings.Join(inName, " AND ")+") OR ("+
				strings.Join(inMeta, " AND ")+"))")
			for range 2 {
				for _, run := range term {
					args = append(args, run)
				}
			}
		}
		q += " AND (v.metadata IS NULL OR " + fmt.Sprintf(contains, "v.metadata") +
			" OR (" + strings.Join(all, " AND ") + "))"
	}
	q += ") ORDER BY p.name LIMIT ?"
	return q, append(args, pg.Limit+1), nil
}

// searchVersionsQuery reads the versions of n packages, each package's in
// the order Versions reads them.
func searchVersionsQuery(n int) string {
	return "SELECT " + versionCols + " FROM package_versions WHERE package_id IN (" +
		strings.TrimSuffix(strings.Repeat("?, ", n), ", ") +
		") ORDER BY package_id, order_key, version"
}
