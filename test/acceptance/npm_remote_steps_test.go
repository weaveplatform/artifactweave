package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerRemoteNPM(sc *godog.ScenarioContext) {
	sc.Step(`^an npm upstream serves "([^"]*)" at "([^"]*)"$`, w.npmUpstream)
}

func (w *world) npmUpstream(name, version string) error {
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	path := name + "/-/" + strings.TrimPrefix(name, "@") + "-" + version + ".tgz"
	if err := w.writeNPMPackage(name, version, "/unused", "npm-upstream"); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(w.dir, "npm-upstream", name+".json"))
	if err != nil {
		return err
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	dist := doc["versions"].(map[string]any)[version].(map[string]any)["dist"].(map[string]any)
	// Relative references exercise the same production rewrite as registry URLs.
	dist["tarball"] = "/" + path
	dist["signatures"] = []any{
		map[string]string{"keyid": "upstream-fixture-key", "sig": "preserved-fixture-signature"},
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	w.remote.Set(
		"/"+name,
		fakeupstream.Resource{
			Body:   raw,
			Header: map[string][]string{"Content-Type": {"application/json"}},
		},
	)
	tgz, err := os.ReadFile(filepath.Join(w.dir, "npm-upstream", name+"-"+version+".tgz"))
	if err != nil {
		return err
	}
	w.remote.Set(
		"/"+path,
		fakeupstream.Resource{
			Body:   tgz,
			Header: map[string][]string{"Content-Type": {"application/octet-stream"}},
		},
	)
	return nil
}
