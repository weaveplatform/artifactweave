package acceptance

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cucumber/godog"
	gomodule "golang.org/x/mod/module"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerRemoteGo(sc *godog.ScenarioContext) {
	sc.Step(`^a private Go upstream serves module "([^"]*)" at "([^"]*)"$`, w.privateGoUpstream)
}

func (w *world) privateGoUpstream(name, version string) error {
	if err := w.writeGoModule(name, version, "go-upstream"); err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "go-upstream", Password: "upstream-pass"}})
	w.closers = append(w.closers, w.remote.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	escaped, err := gomodule.EscapePath(name)
	if err != nil {
		return fmt.Errorf("escape module: %w", err)
	}
	for _, file := range []string{"list", version + ".info", version + ".mod", version + ".zip"} {
		data, readErr := os.ReadFile(filepath.Join(w.dir, "go-upstream", name, "@v", file))
		if readErr != nil {
			return fmt.Errorf("read upstream module: %w", readErr)
		}
		w.remote.Set("/"+escaped+"/@v/"+file, fakeupstream.Resource{Body: data})
	}
	return nil
}
