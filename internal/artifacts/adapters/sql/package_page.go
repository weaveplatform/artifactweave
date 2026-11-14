package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// The queries here count and page in the store what a list's caller may
// read whole (port.PackagePages, port.PackageVersionNames): each walks
// packages_name in name order, or package_versions_order in a package's
// order, so a late page is the index's own walk to its offset rather than
// a read and sort of every name in Go (#1425, #1426). Each must order by
// exactly the indexes' columns, collations included, or the planner sorts.

func (p packages) CountVersions(ctx context.Context, packageID int64) (int, error) {
	var n int
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT COUNT(*) FROM package_versions WHERE package_id = ?"), packageID).Scan(&n)
	return n, p.s.Wrap(fmt.Sprintf("count package %d versions", packageID), err)
}

// versionNamesAtQuery reads only package_versions_order's columns, ordered
// by them after the equality on package_id, forwards or backwards, so both
// engines walk the index to the offset with no sort.
func versionNamesAtQuery(newestFirst bool) string {
	order := " ORDER BY order_key, version"
	if newestFirst {
		order = " ORDER BY order_key DESC, version DESC"
	}
	return "SELECT version FROM package_versions WHERE package_id = ?" + order +
		" LIMIT ? OFFSET ?"
}

func (p packages) VersionNamesAt(
	ctx context.Context,
	packageID int64,
	newestFirst bool,
	offset, limit int,
) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []string
	err := p.s.Rows(
		ctx,
		fmt.Sprintf("list package %d version names", packageID),
		versionNamesAtQuery(newestFirst),
		[]any{packageID, limit, max(offset, 0)},
		func(rs *sql.Rows) error {
			var v string
			err := rs.Scan(&v)
			out = append(out, v)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

// rangeWhere is the condition on packages p that selects r's packages, with
// its arguments, and false when r selects none. The name conditions keep
// the walk of packages_name inside the prefix, as the search's do
// (searchQuery). A package's versions are asked after as a scalar
// subquery rather than EXISTS: PostgreSQL turns an EXISTS into a semi join,
// which it hashes and then sorts the whole range for a late page, where a
// scalar subquery stays a filter on its walk of packages_name, stopping at
// a package's first version that counts. A NuGet range reads each
// version's metadata as JSON: json_extract on SQLite, and on PostgreSQL
// the jsonb cast, which either fails the query on a document that is not
// JSON, as the feed's decoding fails. PostgreSQL casts each document once,
// in a LATERAL subquery that its OFFSET 0 keeps from being merged into the
// conditions: merged, each of the three keys would parse the document
// again, which took a count of 2,000 packages from 5 ms to 14 ms.
func rangeWhere(d dialect.Dialect, r port.PackageRange) (string, []any, bool) {
	if strings.ContainsRune(r.Prefix, 0) {
		return "", nil, false
	}
	where := "p.repo_key = ?"
	args := []any{r.RepoKey}
	if r.Prefix != "" {
		where += " AND p.name >= ?"
		args = append(args, r.Prefix)
		if end, ok := port.SearchPrefixEnd(r.Prefix); ok {
			where += " AND p.name < ?"
			args = append(args, end)
		}
	}
	versions, cond := "package_versions v", ""
	if s := r.Shown; s != nil {
		key := func(k string) string { return "json_extract(v.metadata, '$." + k + "')" }
		contains, semVer2 := "instr(%s, '-') = 0", "COALESCE("+key(port.NuGetSemVer2Key)+", 0) = 0"
		if d.Name == "postgres" {
			versions += " CROSS JOIN LATERAL (SELECT v.metadata::jsonb AS m OFFSET 0) j"
			key = func(k string) string { return "(j.m ->> '" + k + "')" }
			contains = "strpos(%s, '-') = 0"
			semVer2 = "COALESCE(" + key(port.NuGetSemVer2Key) + ", 'false') <> 'true'"
		}
		cond += " AND COALESCE(" + key(port.NuGetPathKey) + ", '') <> ''"
		if !s.Prerelease {
			cond += " AND " + fmt.Sprintf(contains, "COALESCE("+key(port.NuGetVersionKey)+", '')")
		}
		if !s.SemVer2 {
			cond += " AND " + semVer2
		}
	}
	where += " AND (SELECT 1 FROM " + versions + " WHERE v.package_id = p.id" + cond
	return where + " LIMIT 1) IS NOT NULL", args, true
}

// rangeQueries are the count of r's packages and the page of them, by
// name with LIMIT and OFFSET after args, with false when r selects none.
func rangeQueries(
	d dialect.Dialect,
	r port.PackageRange,
) (count, page string, args []any, ok bool) {
	where, args, ok := rangeWhere(d, r)
	return "SELECT COUNT(*) FROM packages p WHERE " + where,
		"SELECT p.id, p.repo_key, p.package_type, p.name, p.created_at FROM packages p WHERE " +
			where + " ORDER BY p.name LIMIT ? OFFSET ?",
		args, ok
}

func (p packages) CountPackages(ctx context.Context, r port.PackageRange) (int, error) {
	count, _, args, ok := rangeQueries(p.s.Dialect(), r)
	if !ok {
		return 0, nil
	}
	var n int
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(count), args...).Scan(&n)
	return n, p.s.Wrap("count packages of "+r.RepoKey, err)
}

func (p packages) PackagesAt(
	ctx context.Context,
	r port.PackageRange,
	offset, limit int,
) ([]model.Package, error) {
	_, page, args, ok := rangeQueries(p.s.Dialect(), r)
	if !ok || limit <= 0 {
		return nil, nil
	}
	var out []model.Package
	err := p.s.Rows(ctx, "page packages of "+r.RepoKey, page,
		append(args, limit, max(offset, 0)),
		func(rs *sql.Rows) error {
			v, err := scanPackage(rs)
			out = append(out, v)
			return err
		})
	return out, err
}

func (p packages) Candidates(
	ctx context.Context,
	packages []model.Package,
) ([]port.SearchCandidate, error) {
	if len(packages) == 0 {
		return nil, nil
	}
	ids := make([]any, len(packages))
	at := make(map[int64]int, len(packages))
	page := make([]port.SearchCandidate, len(packages))
	for i, pkg := range packages {
		ids[i], at[pkg.ID], page[i] = pkg.ID, i, port.SearchCandidate{Package: pkg}
	}
	versions, err := p.versions(
		ctx,
		"read package candidates",
		searchVersionsQuery(len(ids)),
		ids...)
	if err != nil {
		return nil, err
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
	return out, nil
}

// tallyQuery counts each of the first packages by name through
// package_versions_order, whose leading column is package_id, without
// reading a version's row. Only when the repository holds more packages
// than the limit does the order pick which are counted, and then both
// engines walk packages_name to the limit; PostgreSQL may sort fewer, all
// of them, at most the limit. SUM is cast because PostgreSQL sums bigints
// as numeric.
const tallyQuery = "SELECT COUNT(*), CAST(COALESCE(SUM(t.c), 0) AS BIGINT), COALESCE(MAX(t.c), 0) " +
	"FROM (SELECT (SELECT COUNT(*) FROM package_versions v WHERE v.package_id = p.id) AS c " +
	"FROM packages p WHERE p.repo_key = ? ORDER BY p.name LIMIT ?) t"

func (p packages) TallyVersions(
	ctx context.Context,
	repoKey string,
	limit int,
) (port.VersionTally, error) {
	var t port.VersionTally
	if limit <= 0 {
		return t, nil
	}
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(tallyQuery), repoKey, limit).
		Scan(&t.Packages, &t.Versions, &t.MostVersions)
	return t, p.s.Wrap("tally versions of "+repoKey, err)
}

// releasesAtQuery walks packages_name and, within each package,
// package_versions_order backwards, reading only those indexes' columns.
// SQLite's nested loop keeps both orders, so it sorts nothing. PostgreSQL
// would hash the join and sort every release; the LATERAL subquery, kept
// from being merged into a join by its OFFSET 0, makes it loop over
// packages_name instead and sort only each package's versions, already in
// order (an incremental sort).
func releasesAtQuery(d dialect.Dialect) string {
	if d.Name == "postgres" {
		return "SELECT p.id, p.name, v.version FROM packages p CROSS JOIN LATERAL " +
			"(SELECT v.version, v.order_key FROM package_versions v WHERE v.package_id = p.id " +
			"ORDER BY v.order_key DESC, v.version DESC OFFSET 0) v WHERE p.repo_key = ? " +
			"ORDER BY p.name, v.order_key DESC, v.version DESC LIMIT ? OFFSET ?"
	}
	return "SELECT p.id, p.name, v.version FROM packages p " +
		"JOIN package_versions v ON v.package_id = p.id WHERE p.repo_key = ? " +
		"ORDER BY p.name, v.order_key DESC, v.version DESC LIMIT ? OFFSET ?"
}

func (p packages) ReleasesAt(
	ctx context.Context,
	repoKey string,
	offset, limit int,
) ([]port.ReleaseName, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []port.ReleaseName
	err := p.s.Rows(ctx, "page releases of "+repoKey, releasesAtQuery(p.s.Dialect()),
		[]any{repoKey, limit, max(offset, 0)},
		func(rs *sql.Rows) error {
			var r port.ReleaseName
			err := rs.Scan(&r.PackageID, &r.Package, &r.Version)
			out = append(out, r)
			return err //nolint:wrapcheck // wrapped by Rows
		})
	return out, err
}
