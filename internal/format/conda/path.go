package conda

import (
	"path"
	"regexp"
	"strings"
)

const maxPathLength = 1024

var (
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	nameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+]{0,127}$`)
	buildRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._]{0,127}$`)
	subdirRE  = regexp.MustCompile(`^[a-z][a-z0-9]*-[a-z0-9_]{2,32}$`)
)

// PackagePath is one channel-relative conda package file.
type PackagePath struct {
	Path, Channel, Subdir, Filename string
	Name, Version, Build, Extension string
}

// MetadataPath is one channel/subdirectory index path.
type MetadataPath struct {
	Path, Channel, Subdir, Filename string
}

func splitPath(p string) (channel, subdir, filename string, ok bool) {
	if p == "" || len(p) > maxPathLength || p != path.Clean(p) || strings.HasPrefix(p, "/") ||
		strings.ContainsAny(p, "\\\x00\r\n") {
		return "", "", "", false
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 || len(parts) > 6 {
		return "", "", "", false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !segmentRE.MatchString(part) {
			return "", "", "", false
		}
	}
	subdir = parts[len(parts)-2]
	if subdir != "noarch" && !subdirRE.MatchString(subdir) {
		return "", "", "", false
	}
	return strings.Join(parts[:len(parts)-2], "/"), subdir, parts[len(parts)-1], true
}

// ParsePackagePath rejects ambiguous identity and arbitrary repository paths.
// Conda package filenames are <name>-<version>-<build> with one of two suffixes.
func ParsePackagePath(p string) (PackagePath, bool) {
	channel, subdir, filename, ok := splitPath(p)
	if !ok {
		return PackagePath{}, false
	}
	ext := ""
	switch {
	case strings.HasSuffix(filename, ".tar.bz2"):
		ext = ".tar.bz2"
	case strings.HasSuffix(filename, ".conda"):
		ext = ".conda"
	default:
		return PackagePath{}, false
	}
	stem := strings.TrimSuffix(filename, ext)
	last := strings.LastIndexByte(stem, '-')
	if last <= 0 {
		return PackagePath{}, false
	}
	prior := strings.LastIndexByte(stem[:last], '-')
	if prior <= 0 {
		return PackagePath{}, false
	}
	name, version, build := stem[:prior], stem[prior+1:last], stem[last+1:]
	if !nameRE.MatchString(name) || !versionRE.MatchString(version) || !buildRE.MatchString(build) {
		return PackagePath{}, false
	}
	return PackagePath{
		Path: p, Channel: channel, Subdir: subdir, Filename: filename,
		Name: name, Version: version, Build: build, Extension: ext,
	}, true
}

func IsMetadata(p string) bool {
	_, ok := ParseMetadataPath(p)
	return ok
}

func ParseMetadataPath(p string) (MetadataPath, bool) {
	channel, subdir, filename, ok := splitPath(p)
	if !ok {
		return MetadataPath{}, false
	}
	switch filename {
	case "repodata.json", "repodata.json.zst", "repodata.json.bz2",
		"current_repodata.json", "current_repodata.json.zst", "notices.json":
	default:
		return MetadataPath{}, false
	}
	return MetadataPath{Path: p, Channel: channel, Subdir: subdir, Filename: filename}, true
}

func IndexPath(channel, subdir, filename string) string {
	if channel == "" {
		return subdir + "/" + filename
	}
	return channel + "/" + subdir + "/" + filename
}
