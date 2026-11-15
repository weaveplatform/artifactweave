package sql

import "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"

// PackageRangeQueries are CountPackages' and PackagesAt's queries of r,
// with the arguments of the count and of the page at offset of limit, so a
// test can ask the engine for their plans.
func PackageRangeQueries(
	s *Store,
	r port.PackageRange,
	offset, limit int,
) (count, page string, countArgs, pageArgs []any) {
	count, page, args, _ := rangeQueries(s.Dialect(), r)
	return count, page, args, append(append([]any{}, args...), limit, offset)
}

// RangeVersionsQueries are CountRangeVersions' and RangeVersionsAt's
// queries of r, with the arguments of the count and of the page at offset
// of limit.
func RangeVersionsQueries(
	s *Store,
	r port.PackageRange,
	offset, limit int,
) (count, page string, countArgs, pageArgs []any) {
	count, page, args, _ := rangeVersionsQueries(s.Dialect(), r)
	return count, page, args, append(append([]any{}, args...), limit, offset)
}

// TallyQuery is TallyVersions' query.
const TallyQuery = tallyQuery

// ReleasesAtQuery is ReleasesAt's query on s's engine.
func ReleasesAtQuery(s *Store) string { return releasesAtQuery(s.Dialect()) }

// VersionNamesAtQuery is VersionNamesAt's query.
var VersionNamesAtQuery = versionNamesAtQuery
