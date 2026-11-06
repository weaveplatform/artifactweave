package item_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
)

// The item service's copy of maven.IsNonUniqueSnapshot answers as the
// original does, on the snapshot paths of the Maven naming table
// (internal/format/maven/naming_test.go) and on paths of other package
// types, which the copy judges too.
func TestUnitNonUniqueSnapshotMatchesMaven(t *testing.T) {
	for _, p := range []string{
		"com/acme/app/1.0-SNAPSHOT/maven-metadata.xml",
		"com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar",
		"com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT-sources.jar",
		"com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-3.pom",
		"com/acme/app/1.0/app-1.0-SNAPSHOT.jar",
		"com/acme/app/1.0/app-1.0-20261017.090000-3.jar",
		"com/acme/app/1.0-SNAPSHOT",
		"com/acme/app/1.0-SNAPSHOT/",
		"",
		"a.txt",
		"app-1.0-SNAPSHOT.jar",
		"libs/1.0-SNAPSHOT/tool-1.0-SNAPSHOT.zip",
		"libs/1.0-SNAPSHOT/tool-1.0-SNAPSHOT",
		"libs/1.0-SNAPSHOT/notes.txt",
		"libs/1.0-snapshot/tool-1.0-snapshot.zip",
		"npm/app/-/app-1.0.0-SNAPSHOT.tgz",
		"app/1.0-SNAPSHOT/sub/app-1.0-SNAPSHOT.jar",
		"v2/app/manifests/1.0-SNAPSHOT/app-1.0-SNAPSHOT-amd64.json",
	} {
		if got, want := item.NonUniqueSnapshot(p), maven.IsNonUniqueSnapshot(p); got != want {
			t.Errorf("%q: %v, maven.IsNonUniqueSnapshot says %v", p, got, want)
		}
	}
}
