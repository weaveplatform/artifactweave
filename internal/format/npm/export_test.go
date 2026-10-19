package npm

import "github.com/weaveplatform/artifactweave/internal/archive"

// Inspected reads what Inspect returned: the tarball it read, nil for
// content that is none, and whether the write sets its own dist-tags. ok
// is false when Inspect returned no inspection.
func Inspected(v any) (t *Tarball, ownTags, ok bool) {
	in, ok := v.(*inspection)
	if !ok {
		return nil, false, false
	}
	return in.tarball, in.ownTags, true
}

// SetLimits replaces the tarball limits until the returned func restores
// them.
func SetLimits(l archive.Limits) func() {
	old := limits
	limits = l
	return func() { limits = old }
}
