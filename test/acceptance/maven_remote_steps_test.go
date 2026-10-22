package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" //nolint:gosec // Maven checksum wire protocol
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerMavenRemote(sc *godog.ScenarioContext) {
	sc.Step(
		`^a Maven upstream serves parent, transitive, release and snapshot fixtures$`,
		w.mavenUpstream,
	)
	sc.Step(`^the Maven upstream received a cold GET for every fixture$`, w.mavenColdRequests)
}

const remoteMavenGroup = "org/acme/p9remote625/"

func (w *world) mavenUpstream() error {
	if err := w.remoteUpstream("/fixture-root", "Maven fixture"); err != nil {
		return err
	}
	pom := func(a, v, extra string) string {
		return `<project><modelVersion>4.0.0</modelVersion><groupId>org.acme.p9remote625</groupId><artifactId>` + a + `</artifactId><version>` + v + `</version>` + extra + `</project>`
	}
	put := func(p string, data []byte, media string) {
		w.remote.Set(
			"/"+remoteMavenGroup+p,
			fakeupstream.Resource{Body: data, Header: http.Header{"Content-Type": {media}}},
		)
		sum := sha1.Sum(data)
		w.remote.Set(
			"/"+remoteMavenGroup+p+".sha1",
			fakeupstream.Resource{Body: []byte(hex.EncodeToString(sum[:]))},
		)
	}
	put(
		"parent/1.0/parent-1.0.pom",
		[]byte(pom("parent", "1.0", "<packaging>pom</packaging>")),
		"application/xml",
	)
	for _, fixture := range []struct{ artifact, version, fileVersion string }{
		{"transitive", "1.0", "1.0"},
		{"release", "1.0", "1.0"},
		{"unique", "1.1-SNAPSHOT", "1.1-20261021.120000-1"},
		{"nonunique", "2.0-SNAPSHOT", "2.0-SNAPSHOT"},
	} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		f, err := z.Create("META-INF/MANIFEST.MF")
		if err != nil {
			return err
		}
		if _, err = f.Write(
			[]byte("Manifest-Version: 1.0\nFixture: " + fixture.artifact + "\n"),
		); err != nil {
			return err
		}
		if err = z.Close(); err != nil {
			return err
		}
		base := fixture.artifact + "/" + fixture.version + "/" + fixture.artifact + "-" + fixture.fileVersion
		put(base+".jar", b.Bytes(), "application/java-archive")
		extra := ""
		if fixture.artifact == "release" {
			extra = `<parent><groupId>org.acme.p9remote625</groupId><artifactId>parent</artifactId><version>1.0</version><relativePath/></parent><dependencies><dependency><groupId>org.acme.p9remote625</groupId><artifactId>transitive</artifactId><version>1.0</version></dependency></dependencies>`
		}
		put(base+".pom", []byte(pom(fixture.artifact, fixture.version, extra)), "application/xml")
		if strings.HasSuffix(fixture.version, "SNAPSHOT") {
			metadata := `<metadata><groupId>org.acme.p9remote625</groupId><artifactId>` + fixture.artifact + `</artifactId><version>` + fixture.version + `</version><versioning><snapshotVersions>`
			for _, ext := range []string{"pom", "jar"} {
				metadata += `<snapshotVersion><extension>` + ext + `</extension><value>` + fixture.fileVersion + `</value><updated>20261021120000</updated></snapshotVersion>`
			}
			metadata += `</snapshotVersions></versioning></metadata>`
			put(
				fixture.artifact+"/"+fixture.version+"/maven-metadata.xml",
				[]byte(metadata),
				"application/xml",
			)
		}
	}
	return nil
}

func (w *world) mavenColdRequests() error {
	for _, p := range []string{
		"parent/1.0/parent-1.0.pom", "transitive/1.0/transitive-1.0.jar", "release/1.0/release-1.0.jar",
		"unique/1.1-SNAPSHOT/unique-1.1-20261021.120000-1.jar", "nonunique/2.0-SNAPSHOT/nonunique-2.0-SNAPSHOT.jar",
	} {
		if count := w.remote.Count("GET", "/"+remoteMavenGroup+p); count != 1 {
			return fmt.Errorf("Maven cold source %s: got %d GETs, want 1", p, count)
		}
	}
	return nil
}
