// Package swift implements the Swift package registry's stored archive format.
package swift

import (
	"strings"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Package is a Swift registry identity. A release has one immutable source
// archive at the Artifactory layout documented for local Swift repositories.
type Package struct {
	Scope, Name, Version string
}

func Serves(t repo.PackageType) bool { return t == repo.Swift }

func (p Package) ID() string { return p.Scope + "." + p.Name }

func (p Package) ArchivePath() string {
	return p.Scope + "/" + p.Name + "/" + p.Name + "-" + p.Version + ".zip"
}

func (p Package) Valid() bool {
	return ValidScope(p.Scope) && ValidName(p.Name) && ValidVersion(p.Version)
}

// ValidScope and ValidName implement the registry specification's grammar,
// including the different maximum lengths and separator rules.
func ValidScope(s string) bool { return validComponent(s, 39, false) }
func ValidName(s string) bool  { return validComponent(s, 100, true) }

func validComponent(s string, limit int, underscore bool) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	previousSeparator := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		separator := c == '-' || underscore && c == '_'
		if !alnum && !separator || separator && (i == 0 || i == len(s)-1 || previousSeparator) {
			return false
		}
		previousSeparator = separator
	}
	return true
}

func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && strings.Count(core, ".") == 2 && semver.IsValid("v"+v)
}

// ParseArchivePath accepts only the documented local archive layout. It
// never interprets another generic item as a Swift release.
func ParseArchivePath(path string) (Package, bool) {
	var p Package
	parts := strings.Split(path, "/")
	if len(parts) != 3 || !strings.HasSuffix(parts[2], ".zip") {
		return p, false
	}
	p.Scope, p.Name = parts[0], parts[1]
	nameVersion := strings.TrimSuffix(parts[2], ".zip")
	var ok bool
	p.Version, ok = strings.CutPrefix(nameVersion, p.Name+"-")
	return p, ok && p.Valid() && p.ArchivePath() == path
}
