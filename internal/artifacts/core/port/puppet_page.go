package port

import "context"

// PackageVersionNames reads a package's version strings without their
// records. Puppet's, Ansible's and Chef's lists page newest first, the
// order the package index holds for them (model.CompareForge and
// model.CompareGalaxy): a caller who may read every version is counted and
// paged here (CountVersions, VersionNamesAt), and one whose permissions
// name paths has each name checked by the handler, which reads the records
// of its page alone (#1404, #1426).
type PackageVersionNames interface {
	// VersionNames returns the version strings of a package in the order
	// Versions returns its versions: OrderKey, then version string. A
	// package with no versions, or none at all, has none.
	VersionNames(ctx context.Context, packageID int64) ([]string, error)
	// CountVersions returns how many versions a package has.
	CountVersions(ctx context.Context, packageID int64) (int, error)
	// VersionNamesAt returns the version strings at offset of limit in
	// the order VersionNames returns them, or in the reverse order when
	// newestFirst is set.
	VersionNamesAt(
		ctx context.Context,
		packageID int64,
		newestFirst bool,
		offset, limit int,
	) ([]string, error)
}
