package cran

import (
	"path"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

var (
	packageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]*$`)
	versionName = regexp.MustCompile(`^[0-9]+(?:[.-][A-Za-z0-9]+)*$`)
	rVersion    = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	buildName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

const (
	PackagesName     = "PACKAGES"
	PackagesGzipName = "PACKAGES.gz"
)

func Serves(t repo.PackageType) bool { return t == repo.Cran }

// Feed is a terminal R package directory, without a trailing slash.
func Feed(p string) (string, bool) {
	if p == "src/contrib" {
		return p, true
	}
	parts := strings.Split(p, "/")
	if len(parts) < 4 || parts[0] != "bin" ||
		!rVersion.MatchString(parts[len(parts)-1]) ||
		parts[len(parts)-2] != "contrib" {
		return "", false
	}
	if len(parts) == 4 && (parts[1] == "windows" || parts[1] == "linux" || parts[1] == "macosx") {
		return p, true
	}
	if len(parts) == 5 && parts[1] == "macosx" && buildName.MatchString(parts[2]) &&
		parts[2] != "." && parts[2] != ".." {
		return p, true
	}
	return "", false
}

func IndexPath(p string) (feed, name string, ok bool) {
	name = path.Base(p)
	if name != PackagesName && name != PackagesGzipName {
		return "", "", false
	}
	feed, ok = Feed(path.Dir(p))
	return feed, name, ok
}

// PackagePath admits only paths from which a native R client can install.
func PackagePath(p string) (feed, name, version string, ok bool) {
	if strings.HasPrefix(p, "src/contrib/") {
		feed = "src/contrib"
	} else {
		parts := strings.Split(p, "/")
		for i := 4; i < len(parts); i++ {
			if parts[i-2] != "contrib" {
				continue
			}
			candidate := strings.Join(parts[:i], "/")
			if _, valid := Feed(candidate); valid {
				feed = candidate
				break
			}
		}
	}
	if feed == "" || !strings.HasPrefix(p, feed+"/") {
		return "", "", "", false
	}
	relative := strings.TrimPrefix(p, feed+"/")
	for _, segment := range strings.Split(relative, "/") {
		if segment == "" || segment == "." || segment == ".." || !buildName.MatchString(segment) {
			return "", "", "", false
		}
	}
	file := path.Base(p)
	var stem string
	for _, suffix := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".zip"} {
		if strings.HasSuffix(file, suffix) {
			stem = strings.TrimSuffix(file, suffix)
			if feed == "src/contrib" && suffix != ".tar.gz" && suffix != ".tar.bz2" &&
				suffix != ".tar.xz" ||
				strings.HasPrefix(feed, "bin/windows/") && suffix != ".zip" ||
				strings.HasPrefix(feed, "bin/macosx/") && suffix != ".tgz" ||
				strings.HasPrefix(feed, "bin/linux/") && suffix != ".tar.gz" {
				return "", "", "", false
			}
			break
		}
	}
	if stem == "" {
		return "", "", "", false
	}
	name, version, ok = strings.Cut(stem, "_")
	return feed, name, version, ok && packageName.MatchString(name) &&
		versionName.MatchString(version)
}

func IsPackage(p string) bool { _, _, _, ok := PackagePath(p); return ok }

func ValidName(name string) bool { return packageName.MatchString(name) }

func ValidVersion(version string) bool { return versionName.MatchString(version) }
