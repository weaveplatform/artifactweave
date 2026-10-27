package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerJetBrains(sc *godog.ScenarioContext) {
	sc.Step(`^a JetBrains Marketplace fixture serves a compatible plugin$`, w.jetBrainsFixture)
	sc.Step(`^I install the JetBrains fixture plugin with a fresh IDE profile "([^"]*)"$`,
		w.installJetBrainsPlugin)
	sc.Step(`^the JetBrains archive bytes match the upstream fixture$`,
		w.jetBrainsArchiveMatchesFixture)
}

func (w *world) jetBrainsFixture() error {
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, err := z.Create("META-INF/plugin.xml")
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(`<idea-plugin><id>com.weave.fixture</id><name>Weave Fixture</name>` +
		`<version>1.0.0</version><idea-version since-build="252" until-build="253.*"/>` +
		`</idea-plugin>`))
	if err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}
	w.vars["jetbrains-fixture-sha256"] = fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
	w.remote.Set("/api/search/updates/compatible", fakeupstream.Resource{
		Body: []byte(
			`[{"id":12345,"pluginId":98765,"version":"1.0.0","pluginXmlId":"com.weave.fixture"}]`,
		),
		Header: http.Header{"Content-Type": {"application/graphql+json"}},
	})
	w.remote.Set("/files/98765/12345/meta.json", fakeupstream.Resource{
		Body: []byte(
			fmt.Sprintf(
				`{"id":12345,"xmlId":"com.weave.fixture","name":"Weave Fixture","version":"1.0.0","since":"252","until":"253.*","size":%d}`,
				archive.Len(),
			),
		),
		Header: http.Header{"Content-Type": {"application/json"}},
	})
	w.remote.Set("/pluginManager", fakeupstream.Resource{
		Redirect: "/files/98765/12345/weave-fixture.jar",
	})
	w.remote.Set("/files/98765/12345/weave-fixture.jar", fakeupstream.Resource{
		Body: archive.Bytes(), Header: http.Header{"Content-Type": {"application/java-archive"}},
	})
	return nil
}

func (w *world) jetBrainsArchiveMatchesFixture() error {
	got := fmt.Sprintf("%x", sha256.Sum256(w.body))
	want := w.vars["jetbrains-fixture-sha256"]
	if got != want {
		return fmt.Errorf("served JetBrains archive sha256 %s, upstream fixture %s", got, want)
	}
	return nil
}

func (w *world) installJetBrainsPlugin(profile string) error {
	if profile == "" || strings.ContainsAny(profile, "/\\.") {
		return fmt.Errorf("invalid IDE profile")
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	properties := strings.Join([]string{
		"idea.plugins.host=" + w.url + "/artifactory/api/jetbrainsplugins/jetbrains-remote",
		"idea.config.path=/work/idea-" + profile + "/config",
		"idea.system.path=/work/idea-" + profile + "/system",
		"idea.plugins.path=/work/idea-" + profile + "/plugins",
		"idea.log.path=/work/idea-" + profile + "/log",
		"",
	}, "\n")
	name := "idea-" + profile + ".properties"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(properties), 0o600); err != nil {
		return err
	}
	err = w.runContainer(os.Getenv("ARTIFACTWEAVE_JETBRAINS_IMAGE"),
		[]string{"-e", "IDEA_PROPERTIES=/work/" + name},
		[]string{"idea.sh", "installPlugins", "com.weave.fixture"})
	if err != nil {
		return err
	}
	if w.lastRC == 0 {
		return nil
	}
	var requests []string
	for _, method := range []string{"GET", "POST"} {
		for _, request := range w.requests(method) {
			if strings.Contains(request, "/api/jetbrainsplugins/") {
				requests = append(requests, method+" "+request)
			}
		}
	}
	return fmt.Errorf("JetBrains exited %d; Marketplace responses: %v; output: %s",
		w.lastRC, requests, w.body)
}
