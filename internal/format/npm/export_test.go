package npm

import "github.com/weaveplatform/artifactweave/internal/archive"

// SetLimits replaces the tarball limits until the returned func restores
// them.
func SetLimits(l archive.Limits) func() {
	old := limits
	limits = l
	return func() { limits = old }
}
