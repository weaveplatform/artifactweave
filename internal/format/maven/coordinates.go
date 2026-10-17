package maven

import (
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

// Coordinates are a file's place in the package index (decision 0036): the
// layout's reading of its path, the package it belongs to and the version.
type Coordinates struct {
	layout.Coordinates
	// Package is groupId:artifactId, the layout's organisation with dots
	// and its module.
	Package string
	// Version is the name of the version's folder, such as 1.0 or
	// 1.0-SNAPSHOT.
	Version string
}

// mavenLayout is maven-2-default as Artifactory defines it.
var mavenLayout = func() layout.Definition {
	for _, d := range layout.Bundled() {
		if d.Name == "maven-2-default" {
			return d
		}
	}
	panic("maven: no bundled maven-2-default layout")
}()

// IsMavenLayout reports a layout equal to maven-2-default by value, name
// included, as RepoLayoutUtils.isDefaultM2 compares them: only under it does
// Artifactory rename snapshots, map their checksum sidecars and resolve
// -SNAPSHOT downloads to a unique build. A descriptor that edits
// maven-2-default turns those off; a copy of it under another name does not
// turn them on.
func IsMavenLayout(l *layout.Layout) bool {
	if l == nil {
		return false
	}
	d := l.Definition()
	// RepoLayout.equals trims the other side's two path patterns.
	d.ArtifactPathPattern = strings.TrimSpace(d.ArtifactPathPattern)
	d.DescriptorPathPattern = strings.TrimSpace(d.DescriptorPathPattern)
	return d == mavenLayout
}

// Read returns the coordinates l reads from a repository-relative file
// path; ok is false when the file is no file of a version: the layout does
// not fit it or names no organisation, or it is metadata, a checksum or a
// Nexus index file, which belong to no version even where a layout would
// read one.
func Read(l *layout.Layout, fullPath string) (Coordinates, bool) {
	if IsMetadata(fullPath) || IsChecksum(fullPath) || IsIndex(fullPath) {
		return Coordinates{}, false
	}
	lc, ok := l.Read(fullPath)
	if !ok || lc.Org == "" {
		return Coordinates{}, false
	}
	return Coordinates{
		Coordinates: lc,
		Package:     lc.Org + ":" + lc.Module,
		Version:     version(lc, fullPath),
	}, true
}

// version names the version a file belongs to: its version folder's name,
// the base revision with the folder's integration revision, so a unique
// snapshot build is a file of its -SNAPSHOT version and not a version of
// its own. Where the layout puts a file outside any version folder, as
// Maven 1 does every file and gradle-default its Ivy descriptors, the
// file's own integration revision completes the version, which then
// matches its artifacts' folder.
func version(c layout.Coordinates, fullPath string) string {
	if c.FolderItegRev != "" || c.FileItegRev == "" {
		return c.Version()
	}
	// A file with an integration revision in a release folder, such as
	// 1.0/app-1.0-SNAPSHOT.jar, is a file of 1.0, as Artifactory's
	// metadata names a version by the folder holding it.
	if slices.Contains(strings.Split(path.Dir(fullPath), "/"), c.BaseRev) {
		return c.BaseRev
	}
	return c.FileVersion()
}
