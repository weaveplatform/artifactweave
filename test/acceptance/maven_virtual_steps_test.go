package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" //nolint:gosec // Maven wire checksums
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) jvmRecipeOrigins() error {
	if w.vars["recipe-class"] != "virtual" {
		return nil
	}
	jar, descriptor := w.vars["recipe-origin-jar"], w.vars["recipe-origin-descriptor"]
	if n := w.remote.Count("GET", "/"+jar); n != 1 {
		return fmt.Errorf("remote JAR origin: got %d GETs, want1", n)
	}
	if n := w.remote.Count("GET", "/"+descriptor); n != 0 {
		return fmt.Errorf("local descriptor escaped to remote: %d GETs", n)
	}
	return nil
}

func (w *world) jvmRecipeDeployment() error {
	key := w.vars["recipe-repo"]
	if w.vars["recipe-class"] == "virtual" {
		key += "-member"
	}
	jar := "org/acme/widget/1.0.0/widget-1.0.0.jar"
	if strings.HasPrefix(key, "ivy-") {
		jar = "org.acme/widget/1.0.0/jars/widget-1.0.0.jar"
	}
	if err := w.getAsAdmin("/artifactory/api/storage/" + key + "/" + jar); err != nil {
		return err
	}
	return w.statusIs(http.StatusOK)
}

func (w *world) publishIvyRecipe(token string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.recipeClientURL() + "/artifactory/" + w.vars["recipe-repo"]
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	settings := fmt.Sprintf(
		`<ivysettings><settings defaultResolver="aw"/><credentials host="%s" realm="Artifactory Realm" username="${aw.user}" passwd="${aw.token}"/><resolvers><url name="aw"><ivy pattern="%s/[organisation]/[module]/[revision]/ivys/ivy-[revision].xml"/><artifact pattern="%s/[organisation]/[module]/[revision]/[type]s/[artifact]-[revision].[ext]"/></url></resolvers></ivysettings>`,
		u.Hostname(),
		base,
		base,
	)
	if err = os.WriteFile(filepath.Join(dir, "ivysettings.xml"), []byte(settings), 0o600); err != nil {
		return err
	}
	producer := filepath.Join(dir, "ivy-producer")
	if err = os.MkdirAll(producer, 0o700); err != nil {
		return err
	}
	descriptor := `<ivy-module version="2.0"><info organisation="org.acme" module="widget" revision="1.0.0" status="release"/><publications><artifact name="widget" type="jar" ext="jar"/></publications></ivy-module>`
	if err = os.WriteFile(filepath.Join(producer, "ivy-1.0.0.xml"), []byte(descriptor), 0o600); err != nil {
		return err
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, err := z.Create("recipe.txt")
	if err != nil {
		return err
	}
	const marker = "native Ivy publication\n"
	if _, err = entry.Write([]byte(marker)); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(producer, "widget-1.0.0.jar"), archive.Bytes(), 0o600); err != nil {
		return err
	}
	w.vars["recipe-expected"] = marker
	return w.runJVMRecipe(
		[]string{"--entrypoint", "java"},
		[]string{
			"-Duser.home=/work/ivy-publish-home",
			"-Daw.user=admin",
			"-Daw.token=" + w.expand(token),
			"-jar",
			"/opt/ivy.jar",
			"-settings",
			"/work/ivysettings.xml",
			"-cache",
			"/work/ivy-publish-cache",
			"-ivy",
			"/work/ivy-producer/ivy-1.0.0.xml",
			"-publish",
			"aw",
			"-publishpattern",
			"/work/ivy-producer/[artifact]-[revision].[ext]",
			"-deliverto",
			"/work/ivy-producer/ivy-[revision].xml",
			"-revision",
			"1.0.0",
			"-overwrite",
		},
	)
}

func (w *world) registerMavenVirtual(sc *godog.ScenarioContext) {
	sc.Step(`^a (maven|gradle) deployment repository "([^"]*)" of class "([^"]*)" is ready$`, w.mavenFamilyDeployment)
	sc.Step(`^a mixed Maven virtual deployment fixture is ready$`, w.mavenVirtualFixture)
	sc.Step(`^the Maven virtual consumer uses remote, range and shared snapshot dependencies$`, w.mavenVirtualConsumer)
	sc.Step(`^Maven resolved the expected mixed origins without POM repository escape$`, w.mavenVirtualResolved)
}

const mavenVirtualGroup = "org/acme/p10virtual1559/"

func mavenVirtualJar(marker string) ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	entry, err := z.Create("origin.txt")
	if err != nil {
		return nil, err
	}
	if _, err = entry.Write([]byte(marker)); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func mavenVirtualPOM(artifact, version, extra string) []byte {
	return []byte(
		`<project><modelVersion>4.0.0</modelVersion><groupId>org.acme.p10virtual1559</groupId><artifactId>` + artifact + `</artifactId><version>` + version + `</version>` + extra + `</project>`,
	)
}

func mavenVirtualSnapshot(fileVersion, updated string) []byte {
	out := `<metadata><groupId>org.acme.p10virtual1559</groupId><artifactId>shared</artifactId><version>1.0-SNAPSHOT</version><versioning><lastUpdated>` + updated + `</lastUpdated><snapshot><timestamp>` + strings.Split(fileVersion, "-")[1] + `</timestamp><buildNumber>` + strings.Split(fileVersion, "-")[2] + `</buildNumber></snapshot><snapshotVersions>`
	for _, ext := range []string{"pom", "jar"} {
		out += `<snapshotVersion><extension>` + ext + `</extension><value>` + fileVersion + `</value><updated>` + updated + `</updated></snapshotVersion>`
	}
	return []byte(out + `</snapshotVersions></versioning></metadata>`)
}

func (w *world) mavenVirtualFixture() error {
	if err := w.remoteUpstream("/fixture-root", "Maven mixed virtual fixture"); err != nil {
		return err
	}
	for _, c := range []struct {
		key    string
		config map[string]any
	}{
		{"mvn-member", map[string]any{"rclass": "local", "packageType": "maven"}},
		{"mvn-remote", map[string]any{"rclass": "remote", "packageType": "maven", "url": w.remote.URL, "remoteRepoChecksumPolicyType": "fail", "rejectInvalidJars": true}},
		{"mvn-virtual", map[string]any{"rclass": "virtual", "packageType": "maven", "repositories": []string{"mvn-member", "mvn-remote"}, "defaultDeploymentRepo": "mvn-member"}},
		{"mvn-no-ddr", map[string]any{"rclass": "virtual", "packageType": "maven", "repositories": []string{"mvn-member", "mvn-remote"}}},
	} {
		if err := w.recipeRepoPut(c.key, c.config); err != nil {
			return err
		}
	}
	local := map[string][]byte{}
	put := func(files map[string][]byte, artifact, version, fileVersion, marker, extra string) error {
		jar, err := mavenVirtualJar(marker)
		if err != nil {
			return err
		}
		base := mavenVirtualGroup + artifact + "/" + version + "/" + artifact + "-" + fileVersion
		files[base+".jar"] = jar
		files[base+".pom"] = mavenVirtualPOM(artifact, version, extra)
		return nil
	}
	if err := put(local, "range", "1.0", "1.0", "local range1", ""); err != nil {
		return err
	}
	if err := put(local, "shared", "1.0-SNAPSHOT", "1.0-20261019.120000-1", "local snapshot1", ""); err != nil {
		return err
	}
	if err := put(local, "transitive", "1.0", "1.0", "local transitive", ""); err != nil {
		return err
	}
	if err := w.recipeDeploy("mvn-member", local); err != nil {
		return err
	}
	remote := map[string][]byte{}
	escape := `<profiles><profile><id>poison</id><activation><activeByDefault>true</activeByDefault></activation><repositories><repository><id>escape</id><url>` + w.remote.URL + `/escape/</url></repository></repositories><dependencies><dependency><groupId>org.acme.p10virtual1559</groupId><artifactId>transitive</artifactId><version>1.0</version></dependency></dependencies></profile></profiles>`
	if err := put(remote, "remote", "1.0", "1.0", "remote only", escape); err != nil {
		return err
	}
	if err := put(remote, "range", "2.0", "2.0", "remote range2", ""); err != nil {
		return err
	}
	// Keep the remote build unmistakably newer than local metadata generated
	// during this pinned fixture's run, rather than relying on a clock tie.
	if err := put(remote, "shared", "1.0-SNAPSHOT", "1.0-20300101.120000-2", "remote snapshot2", ""); err != nil {
		return err
	}
	remote[mavenVirtualGroup+"range/maven-metadata.xml"] = []byte(
		`<metadata><groupId>org.acme.p10virtual1559</groupId><artifactId>range</artifactId><versioning><latest>2.0</latest><release>2.0</release><versions><version>2.0</version></versions><lastUpdated>20300101120000</lastUpdated></versioning></metadata>`,
	)
	remote[mavenVirtualGroup+"shared/1.0-SNAPSHOT/maven-metadata.xml"] = mavenVirtualSnapshot("1.0-20300101.120000-2", "20300101120000")
	for path, data := range remote {
		w.remote.Set("/"+path, fakeupstream.Resource{Body: data})
		sum := sha1.Sum(data)
		w.remote.Set("/"+path+".sha1", fakeupstream.Resource{Body: []byte(hex.EncodeToString(sum[:]))})
	}
	return nil
}

func (w *world) mavenVirtualConsumer() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "app", "pom.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	extra := `<dependency><groupId>org.acme.p10virtual1559</groupId><artifactId>remote</artifactId><version>1.0</version></dependency><dependency><groupId>org.acme.p10virtual1559</groupId><artifactId>range</artifactId><version>[1.0,3.0)</version></dependency><dependency><groupId>org.acme.p10virtual1559</groupId><artifactId>shared</artifactId><version>1.0-SNAPSHOT</version></dependency>`
	return os.WriteFile(path, []byte(strings.Replace(string(data), "</dependencies>", extra+"</dependencies>", 1)), 0o600)
}

func (w *world) mavenVirtualResolved() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, v := range []struct{ path, marker string }{
		{"remote/1.0/remote-1.0.jar", "remote only"}, {"range/2.0/range-2.0.jar", "remote range2"}, {"shared/1.0-SNAPSHOT/shared-1.0-SNAPSHOT.jar", "remote snapshot2"}, {"transitive/1.0/transitive-1.0.jar", "local transitive"},
	} {
		data, err := os.ReadFile(filepath.Join(dir, "fresh", filepath.FromSlash(mavenVirtualGroup+v.path)))
		if err != nil {
			return err
		}
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		found := false
		for _, f := range z.File {
			if f.Name == "origin.txt" {
				r, err := f.Open()
				if err != nil {
					return err
				}
				b, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil {
					return err
				}
				found = string(b) == v.marker
			}
		}
		if !found {
			return fmt.Errorf("Maven resolved wrong origin for %s", v.path)
		}
	}
	for _, path := range []string{"remote/1.0/remote-1.0.jar", "range/2.0/range-2.0.jar", "shared/1.0-SNAPSHOT/shared-1.0-20300101.120000-2.jar"} {
		if n := w.remote.Count("GET", "/"+mavenVirtualGroup+path); n != 1 {
			return fmt.Errorf("cold remote %s: got%d GETs, want1", path, n)
		}
	}
	pom, err := os.ReadFile(filepath.Join(dir, "fresh", filepath.FromSlash(mavenVirtualGroup+"remote/1.0/remote-1.0.pom")))
	if err != nil {
		return err
	}
	if strings.Contains(string(pom), "/escape/") {
		return fmt.Errorf("active POM repository reference was not cleaned")
	}
	for _, r := range w.remote.Requests() {
		if strings.HasPrefix(r.Path, "/escape/") {
			return fmt.Errorf("Maven escaped the virtual: %s", r.Path)
		}
	}
	return nil
}

func (w *world) sbtPayload(key string) string {
	if key == "sbt-v" && w.vars["sbt-virtual-published"] == "true" {
		return "virtual sbt publication\n"
	}
	return "recipe fixture\n"
}

func (w *world) sbtMixedUpstream() error {
	if err := w.remoteUpstream("/fixture-root", "sbt mixed member fixture"); err != nil {
		return err
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["sbt-mixed"] = "true"
	for _, fixture := range []struct{ name, marker string }{{"widget", "recipe fixture\n"}, {"remote-widget", "remote sbt dependency\n"}} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		entry, err := z.Create("payload.txt")
		if err != nil {
			return err
		}
		if _, err = entry.Write([]byte(fixture.marker)); err != nil {
			return err
		}
		if err = z.Close(); err != nil {
			return err
		}
		base := "/org/weave/recipe/" + fixture.name + "/1.0.0/" + fixture.name + "-1.0.0"
		pom := []byte(
			`<project><modelVersion>4.0.0</modelVersion><groupId>org.weave.recipe</groupId><artifactId>` + fixture.name + `</artifactId><version>1.0.0</version></project>`,
		)
		for ext, data := range map[string][]byte{".jar": b.Bytes(), ".pom": pom} {
			w.remote.Set(base+ext, fakeupstream.Resource{Body: data})
			sum := sha1.Sum(data)
			w.remote.Set(base+ext+".sha1", fakeupstream.Resource{Body: []byte(hex.EncodeToString(sum[:]))})
		}
	}
	return nil
}

func (w *world) sbtMixedOrigins() error {
	for _, fixture := range []struct {
		artifact string
		want     int
	}{{"widget", 0}, {"remote-widget", 1}} {
		path := "/org/weave/recipe/" + fixture.artifact + "/1.0.0/" + fixture.artifact + "-1.0.0.jar"
		if got := w.remote.Count("GET", path); got != fixture.want {
			return fmt.Errorf("sbt virtual origin %s: got%d GETs, want%d", path, got, fixture.want)
		}
	}
	return nil
}

func (w *world) mavenFamilyDeployment(kind, key, class string) error {
	config := map[string]any{"rclass": class, "packageType": kind}
	if class == "virtual" {
		member := key + "-member"
		remote := key + "-remote"
		if err := w.remoteUpstream("/fixture-root", "JFrog virtual deployment fixture"); err != nil {
			return err
		}
		if err := w.recipeRepoPut(member, map[string]any{"rclass": "local", "packageType": kind}); err != nil {
			return err
		}
		if err := w.recipeRepoPut(remote, map[string]any{"rclass": "remote", "packageType": kind, "url": w.remote.URL}); err != nil {
			return err
		}
		config["repositories"] = []string{member, remote}
		config["defaultDeploymentRepo"] = member
	}
	return w.recipeRepoPut(key, config)
}

func (w *world) sbtArtifact(key string) string {
	if key == "sbt-v" && w.vars["sbt-virtual-published"] == "true" {
		return "widget-1.1.0.jar"
	}
	return "widget-1.0.0.jar"
}

// The configured JVM image selects its own architecture, just as Maven's
// ordinary native invocations do. Other recipes retain their existing runner.
func (w *world) runJVMRecipe(flags, argv []string) error {
	return w.runRecipeClient(w.mavenImage, "", flags, argv)
}
