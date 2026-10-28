package cran

import "strings"

// LegacyArchiveTarget recognizes the source archive layout used by older
// Artifactory releases. The package and version directories must agree with
// the filename; unrelated paths and canonical archives are left alone.
func LegacyArchiveTarget(p string) (string, bool) {
	const prefix = "src/contrib/Archive/"
	if !strings.HasPrefix(p, prefix) {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(p, prefix), "/")
	if len(parts) != 3 {
		return "", false
	}
	_, name, version, ok := PackagePath(p)
	if !ok || name != parts[0] || version != parts[1] {
		return "", false
	}
	return prefix + name + "/" + parts[2], true
}
