package port

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// PackagePages counts and pages a repository's packages in the store, for
// the lists whose caller may read every path of the repository
// (accessapi.Decider.AllowsAll): npm and NuGet search, NuGet's
// autocomplete and V2 feeds, Chef's catalog and Puppet's whole-repository
// lists (#1425, #1426). Such a caller sees every package with a version, so the count and
// the page are exact in the store; a caller whose permissions name paths
// still has each version checked by the handler, over SearchNpm,
// SearchNuGet, List or VersionNames.
type PackagePages interface {
	// CountPackages returns how many packages of r have a version r
	// admits.
	CountPackages(ctx context.Context, r PackageRange) (int, error)
	// PackagesAt returns the packages CountPackages counts that come at
	// offset of limit by name, in byte order, as List orders them.
	PackagesAt(ctx context.Context, r PackageRange, offset, limit int) ([]model.Package, error)
	// CountRangeVersions returns how many versions of r's packages r
	// admits: what a NuGet V2 feed of every version counts.
	CountRangeVersions(ctx context.Context, r PackageRange) (int, error)
	// RangeVersionsAt returns the versions CountRangeVersions counts at
	// offset of limit in a list of them by package name and then, within a
	// package, in Versions' order: a NuGet V2 feed's page.
	RangeVersionsAt(ctx context.Context, r PackageRange, offset, limit int) ([]ReleaseName, error)
	// Candidates returns packages, in their order, each with every one of
	// its versions in Versions' order, in one read; a package with no
	// version left, deleted since it was paged, is left out, as SearchNpm
	// leaves it out.
	Candidates(ctx context.Context, packages []model.Package) ([]SearchCandidate, error)
	// TallyVersions counts, over the first limit packages of repoKey by
	// name, those without a version included, how many there are, how
	// many versions they hold and the most one of them holds: the caps a
	// whole-repository list answers 503 over.
	TallyVersions(ctx context.Context, repoKey string, limit int) (VersionTally, error)
	// ReleasesAt returns the versions of repoKey's packages at offset of
	// limit in a list of them by package name and then, within a package,
	// from its highest OrderKey down: Puppet's whole-repository release
	// list, newest first.
	ReleasesAt(ctx context.Context, repoKey string, offset, limit int) ([]ReleaseName, error)
}

// CandidatesAt returns the packages of r at offset of limit
// (PackagePages.PackagesAt), each with its versions (Candidates), and how
// many packages the page held, which is fewer than limit only past the
// range's end.
func CandidatesAt(
	ctx context.Context,
	s PackagePages,
	r PackageRange,
	offset, limit int,
) ([]SearchCandidate, int, error) {
	packages, err := s.PackagesAt(ctx, r, offset, limit)
	if err != nil {
		return nil, 0, err //nolint:wrapcheck // the store's error names the read and the repository
	}
	if len(packages) == 0 {
		return nil, 0, nil
	}
	found, err := s.Candidates(ctx, packages)
	return found, len(packages), err
}

// PackageRange is the packages of RepoKey that PackagePages counts and
// pages: those whose names start with Prefix that have a version Shown
// admits, every version when Shown is nil.
type PackageRange struct {
	RepoKey string
	// Prefix, when not empty, keeps only the names that start with it, in
	// bytes, as NuGetSearch's Prefix does; a Prefix with a NUL byte keeps
	// none.
	Prefix string
	// Shown, when set, admits only the versions a NuGet feed shows.
	Shown *NuGetShown
}

// NuGetShown is which versions a NuGet feed shows a caller who may read
// all of them (decision 0038), from each version's stored metadata, as
// nugetfmt.Metadata writes it: those whose .nupkg is stored (a non-empty
// NuGetPathKey), and of those the pre-releases (a hyphen in
// NuGetVersionKey) only with Prerelease, and the SemVer 2.0.0 versions
// (NuGetSemVer2Key true) only with SemVer2. A version without metadata
// has no .nupkg. A store reads the metadata as JSON, so one that is not
// fails the read, as the feed's own decoding fails on it.
type NuGetShown struct {
	Prerelease, SemVer2 bool
}

// The keys of a NuGet version's stored metadata that NuGetShown reads. They
// must match nugetfmt.Metadata's JSON names, which the nuget package's
// tests check.
const (
	NuGetPathKey    = "path"
	NuGetVersionKey = "version"
	NuGetSemVer2Key = "semver2"
)

// VersionTally is what TallyVersions counts.
type VersionTally struct {
	// Packages is how many packages it counted, at most its limit.
	Packages int
	// Versions is how many versions they hold, and MostVersions the most
	// one of them holds.
	Versions, MostVersions int
}

// ReleaseName is a version ReleasesAt or RangeVersionsAt lists: its
// package's ID and name, and the version string, whose record
// PackageStore.Version reads.
type ReleaseName struct {
	PackageID int64
	Package   string
	Version   string
}
