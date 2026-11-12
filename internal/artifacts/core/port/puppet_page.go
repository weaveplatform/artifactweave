package port

import "context"

// PackageVersionNames reads a package's version strings without their
// records. Puppet's, Ansible's and Chef's lists page in an order the
// package index does not hold, their own semantic version order, and count
// only the versions the caller may read, which read permission decides
// path by path: so they order and count every version by its name and read
// the records of their page alone (#1404).
type PackageVersionNames interface {
	// VersionNames returns the version strings of a package in the order
	// Versions returns its versions: OrderKey, then version string. A
	// package with no versions, or none at all, has none.
	VersionNames(ctx context.Context, packageID int64) ([]string, error)
}
