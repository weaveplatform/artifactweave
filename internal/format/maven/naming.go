package maven

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The naming predicates are Artifactory's MavenNaming (Artifactory OSS
// 6.20, base/config/src/main/java/org/artifactory/mime/MavenNaming.java at
// b99e5649bcad): they read a path's name alone, whatever the repository's
// layout, which is how Artifactory tells Maven's files apart in every
// repository of the Maven group.

// Names Maven gives its files.
const (
	// MetadataFile is maven-metadata.xml, at the snapshot, artifact or
	// plugin level.
	MetadataFile = "maven-metadata.xml"
	// Snapshot is the integration revision of a non-unique snapshot.
	Snapshot = "SNAPSHOT"
	// SnapshotSuffix ends a snapshot version, as in 1.0-SNAPSHOT.
	SnapshotSuffix = "-" + Snapshot
	// nexusIndexPrefix begins the files of a Nexus repository index.
	nexusIndexPrefix = "nexus-maven-repository-index"
)

// uniqueSnapshotName is MavenNaming's UNIQUE_SNAPSHOT_NAME_PATTERN, a
// unique snapshot's file name such as app-1.0-20261017.090000-1.jar:
// group 4 is the timestamp and build number, 5 the timestamp and 6 the
// build number. The '.' in the timestamp is any character, as in
// Artifactory.
var uniqueSnapshotName = regexp.MustCompile(
	`^((.+)-(.+))-(([0-9]{8}.[0-9]{6})-([0-9]+))[.-].+$`)

// IsMetadata reports a maven-metadata.xml file.
func IsMetadata(p string) bool { return path.Base(p) == MetadataFile }

// IsSnapshotMetadata reports the maven-metadata.xml of a snapshot version
// folder.
func IsSnapshotMetadata(p string) bool {
	return IsMetadata(p) && strings.HasSuffix(path.Dir(p), SnapshotSuffix)
}

// IsSnapshotVersion reports a non-unique snapshot version, such as
// 1.0-SNAPSHOT.
func IsSnapshotVersion(version string) bool { return strings.HasSuffix(version, SnapshotSuffix) }

// IsNonUniqueSnapshot reports a file of a snapshot folder named with
// -SNAPSHOT before its classifier or extension, such as
// 1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar.
func IsNonUniqueSnapshot(p string) bool {
	if !inSnapshotFolder(p) {
		return false
	}
	name := path.Base(p)
	return strings.Contains(name, SnapshotSuffix+".") || strings.Contains(name, SnapshotSuffix+"-")
}

// IsUniqueSnapshot reports a file of a snapshot folder whose name carries
// a timestamp and build number, such as
// 1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar.
func IsUniqueSnapshot(p string) bool {
	return inSnapshotFolder(p) && IsUniqueSnapshotName(path.Base(p))
}

// IsUniqueSnapshotName reports a unique snapshot's file name.
func IsUniqueSnapshotName(name string) bool { return uniqueSnapshotName.MatchString(name) }

func inSnapshotFolder(p string) bool { return strings.HasSuffix(path.Dir(p), SnapshotSuffix) }

// IsSnapshot reports a snapshot file, unique or not, or a snapshot version
// folder.
func IsSnapshot(p string) bool {
	if IsNonUniqueSnapshot(p) || IsUniqueSnapshot(p) {
		return true
	}
	// A path ending in its version folder, with or without a slash.
	if i := strings.Index(p, SnapshotSuffix+"/"); i > 0 &&
		strings.LastIndexByte(p, '/') == i+len(SnapshotSuffix) {
		return true
	}
	return strings.HasSuffix(p, SnapshotSuffix)
}

// UniqueBuild is the timestamp and build number a unique snapshot's file
// name carries.
type UniqueBuild struct {
	// Timestamp is yyyyMMdd.HHmmss in UTC.
	Timestamp string
	Number    int
}

// String is the build's part of a file name, as in 20261017.090000-1.
func (b UniqueBuild) String() string { return b.Timestamp + "-" + strconv.Itoa(b.Number) }

// ParseUniqueBuild reads the build from a unique snapshot's file name; ok
// is false for any other name.
func ParseUniqueBuild(name string) (UniqueBuild, bool) {
	m := uniqueSnapshotName.FindStringSubmatch(name)
	if m == nil {
		return UniqueBuild{}, false
	}
	n, err := strconv.Atoi(m[6])
	if err != nil {
		// More digits than an int holds: no build Maven would number.
		return UniqueBuild{}, false
	}
	return UniqueBuild{Timestamp: m[5], Number: n}, true
}

// IsPom reports a POM, by its extension, as Artifactory's mime type for
// .pom does.
func IsPom(p string) bool { return strings.EqualFold(path.Ext(p), ".pom") }

// IsClientPom reports the pom.xml a project keeps beside its sources, which
// a jar carries under META-INF/maven.
func IsClientPom(p string) bool { return strings.EqualFold(path.Base(p), "pom.xml") }

// IsChecksum reports a checksum sidecar by its extension, with the
// algorithms item.Sidecar accepts.
func IsChecksum(p string) bool {
	switch path.Ext(p) {
	case ".sha1", ".md5", ".sha256", ".sha512":
		return path.Base(p) != path.Ext(p)
	}
	return false
}

// IsMetadataChecksum reports a checksum sidecar of maven-metadata.xml.
func IsMetadataChecksum(p string) bool {
	return IsChecksum(p) && IsMetadata(strings.TrimSuffix(p, path.Ext(p)))
}

// IsIndex reports a file of a Nexus repository index.
func IsIndex(p string) bool { return strings.HasPrefix(path.Base(p), nexusIndexPrefix) }
