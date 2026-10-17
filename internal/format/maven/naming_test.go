package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/maven"
)

// The predicates answer as Artifactory's MavenNaming does for the same
// paths.
func TestNaming(t *testing.T) {
	type answers struct {
		metadata, snapshotMetadata, nonUnique, unique, snapshot bool
		pom, clientPom, checksum, metadataChecksum, index       bool
	}
	for _, tc := range []struct {
		path string
		want answers
	}{
		{"com/acme/app/1.0/app-1.0.jar", answers{}},
		{"com/acme/app/1.0/app-1.0.pom", answers{pom: true}},
		{"com/acme/app/1.0/app-1.0.POM", answers{pom: true}},
		{"com/acme/app/1.0/app-1.0.jar.sha1", answers{checksum: true}},
		{"com/acme/app/1.0/app-1.0.jar.sha512", answers{checksum: true}},
		{"com/acme/app/1.0/.md5", answers{}},
		{"com/acme/app/maven-metadata.xml", answers{metadata: true}},
		{"com/acme/app/maven-metadata.xml.sha256", answers{checksum: true, metadataChecksum: true}},
		{"com/acme/app/1.0-SNAPSHOT/maven-metadata.xml", answers{
			metadata: true, snapshotMetadata: true, snapshot: true,
		}},
		{"com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", answers{nonUnique: true, snapshot: true}},
		{"com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT-sources.jar", answers{
			nonUnique: true, snapshot: true,
		}},
		{"com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-3.pom", answers{
			unique: true, snapshot: true, pom: true,
		}},
		// A snapshot name outside a snapshot folder is neither, as in
		// Artifactory, which reads the folder first.
		{"com/acme/app/1.0/app-1.0-SNAPSHOT.jar", answers{}},
		{"com/acme/app/1.0/app-1.0-20261017.090000-3.jar", answers{}},
		{"com/acme/app/1.0-SNAPSHOT", answers{snapshot: true}},
		{"com/acme/app/1.0-SNAPSHOT/", answers{snapshot: true}},
		{"com/acme/app/pom.xml", answers{clientPom: true}},
		{".index/nexus-maven-repository-index.gz", answers{index: true}},
	} {
		got := answers{
			metadata:         maven.IsMetadata(tc.path),
			snapshotMetadata: maven.IsSnapshotMetadata(tc.path),
			nonUnique:        maven.IsNonUniqueSnapshot(tc.path),
			unique:           maven.IsUniqueSnapshot(tc.path),
			snapshot:         maven.IsSnapshot(tc.path),
			pom:              maven.IsPom(tc.path),
			clientPom:        maven.IsClientPom(tc.path),
			checksum:         maven.IsChecksum(tc.path),
			metadataChecksum: maven.IsMetadataChecksum(tc.path),
			index:            maven.IsIndex(tc.path),
		}
		if got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.path, got, tc.want)
		}
	}
	if !maven.IsSnapshotVersion("1.0-beta-SNAPSHOT") || maven.IsSnapshotVersion("1.0") {
		t.Error("snapshot versions")
	}
}

func TestParseUniqueBuild(t *testing.T) {
	for _, tc := range []struct {
		name string
		want maven.UniqueBuild
		ok   bool
	}{
		{"app-1.0-20261017.090000-3.jar", maven.UniqueBuild{"20261017.090000", 3}, true},
		{"app-1.0-20261017.090000-12-sources.jar", maven.UniqueBuild{"20261017.090000", 12}, true},
		{"app-1.0-SNAPSHOT.jar", maven.UniqueBuild{}, false},
		{"app-1.0-20261017.090000-99999999999999999999.jar", maven.UniqueBuild{}, false},
	} {
		got, ok := maven.ParseUniqueBuild(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %+v %v", tc.name, got, ok)
		}
		if ok != maven.IsUniqueSnapshotName(tc.name) && tc.ok {
			t.Errorf("%s: IsUniqueSnapshotName disagrees", tc.name)
		}
	}
	if got := (maven.UniqueBuild{"20261017.090000", 3}).String(); got != "20261017.090000-3" {
		t.Error(got)
	}
}
