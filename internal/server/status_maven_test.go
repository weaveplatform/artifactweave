package server

import (
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// mavenRepos are Maven's fixture repositories: maven-local, maven-remote and
// maven-virtual, and maven-releases, a local repository that does not
// handle snapshots.
var mavenRepos = append(fixtureRepos("maven", repo.Maven), repo.Config{
	Key: "maven-releases", Class: repo.Local, PackageType: repo.Maven, HandleSnapshots: new(bool),
})

// Routes the Maven rules answer on: the item routes every client deploys
// and downloads through.
const (
	mavenPut = "PUT /artifactory/{repoKey}/{filePath}"
	mavenGet = "GET /artifactory/{repoKey}/{filePath}"
)

// mavenPOM is a POM of g:a:v.
func mavenPOM(g, a, v string) string {
	return `<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion>` +
		`<groupId>` + g + `</groupId><artifactId>` + a + `</artifactId><version>` + v +
		`</version></project>`
}

// mavenStatusPart is Maven's share of the status table, which its pull
// requests (#276–#284) fill in.
var mavenStatusPart = statusPart{
	rows: func() []statusCase {
		const (
			local    = "/artifactory/maven-local/com/acme/app/"
			releases = "/artifactory/maven-releases/com/acme/app/"
		)
		sha1 := strings.Repeat("0", 40)
		return []statusCase{
			// The POM consistency check (#278).
			{mavenPut, local + "1.0/app-1.0.pom", "admin", mavenPOM("com.acme", "app", "1.0"), 201},
			{
				mavenPut,
				local + "2.0/app-2.0.pom",
				"admin",
				mavenPOM("com.other", "app", "2.0"),
				409,
			},
			{mavenPut, local + "3.0/app-3.0.pom", "admin", "not a POM", 409},
			{mavenPut, local + "1.0/app-1.0.pom", "user", mavenPOM("com.acme", "app", "1.0"), 403},
			// Uploaded metadata: the snapshot level is dropped, the artifact
			// level kept, and every metadata checksum is the server's.
			{mavenPut, local + "1.0-SNAPSHOT/maven-metadata.xml", "admin", "<metadata/>", 202},
			{mavenPut, local + "maven-metadata.xml", "admin", "<metadata/>", 201},
			{mavenPut, local + "maven-metadata.xml.sha1", "admin", sha1, 200},
			{mavenPut, local + "1.0-SNAPSHOT/maven-metadata.xml.sha512", "admin", sha1, 200},
			// Sidecars: a missing file, an oversized body and a mismatch.
			{mavenPut, local + "1.0/app-1.0.war.sha1", "admin", sha1, 404},
			{mavenPut, local + "1.0/app-1.0.pom.sha1", "admin", strings.Repeat("a", 1025), 409},
			{mavenPut, local + "1.0/app-1.0.pom.sha1", "admin", sha1, 409},
			{mavenPut, local + "1.0/app-1.0.jar.md5", "admin", strings.Repeat("0", 32), 409},
			// The release and snapshot policy, on deploy and download.
			{mavenPut, releases + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "admin", "x", 409},
			{mavenPut, releases + "1.0/app-1.0.jar", "admin", "x", 201},
			{mavenGet, releases + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "admin", "", 409},
			{mavenGet, releases + "1.0/app-1.0.jar", "admin", "", 200},
		}
	},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		ctx := context.Background()
		c, err := db.Repos().Get(ctx, "maven-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&item.Service{Store: db, Blobs: blobs}).Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: "com/acme/app/1.0/app-1.0.jar"},
			Body:   strings.NewReader("jar"),
		}); err != nil {
			t.Fatal(err)
		}
	},
}
