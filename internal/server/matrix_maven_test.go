package server

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// mavenMatrixPart is Maven's share of the route × principal matrix, which
// its pull requests (#276–#284) fill in. Each rule answers a caller who may
// write (or read) the path; the rest are refused before it applies.
var mavenMatrixPart = matrixPart{
	rows: func() []matrixRow {
		const (
			local    = "/artifactory/maven-local/work/"
			releases = "/artifactory/maven-releases/work/"
		)
		write, read := types.ActionWrite, types.ActionRead
		writes := holds("maven-local", "work/", write)
		sha1 := strings.Repeat("0", 40)
		return []matrixRow{
			row(mavenPut, local+"app-{p}/1.0/app-{p}-1.0.pom", mavenPOM("work", "app-{p}", "1.0"),
				writes, 201),
			row(mavenPut, local+"bad-{p}/1.0/bad-{p}-1.0.pom", mavenPOM("other", "bad-{p}", "1.0"),
				writes, 409),
			row(mavenPut, local+"app/1.0-SNAPSHOT/maven-metadata.xml", "<metadata/>", writes, 202),
			row(mavenPut, local+"app/maven-metadata.xml.sha1", sha1, writes, 200),
			row(mavenPut, local+"none-{p}.jar.sha1", sha1, writes, 404),
			row(mavenPut, releases+"app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "x",
				holds("maven-releases", "work/", write), 409),
			row(mavenGet, releases+"app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "",
				holds("maven-releases", "work/", read), 409),
			// calculateMetadata needs every action Manage brings on the
			// folder; generatePom reads the jar and deploys its POM (#282).
			row(mavenCalculate, "/artifactory/api/maven/calculateMetadata/maven-local/work/app",
				"", holds("maven-local", "work/app", calculateMetadataActions), 200),
			row(
				mavenGenerate,
				"/artifactory/api/maven/generatePom/maven-local/work/gen-{p}/1.0/gen-{p}-1.0.jar",
				"",
				holds("maven-local", "work/", read|write),
				200,
			).seeding(
				func(t *testing.T, f *matrixFixture, p string) {
					t.Helper()
					f.deploy(t, "maven-local", "work/gen-"+p+"/1.0/gen-"+p+"-1.0.jar", []byte(p))
				}),
		}
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		f.deploy(t, "maven-local", "work/app/1.0/app-1.0.jar", []byte("jar"))
	},
}
