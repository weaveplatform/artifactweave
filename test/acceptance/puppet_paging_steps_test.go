package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// registerPuppetPaging registers the steps of puppet_paging.feature: a
// module with more releases than one page of its release list holds.
func (w *world) registerPuppetPaging(sc *godog.ScenarioContext) {
	sc.Step(`^(\d+) releases of Puppet module "([^"]*)" are ready under "([^"]*)"$`,
		w.puppetReleases)
	sc.Step(`^I install Puppet module "([^"]*)" version "([^"]*)" from "([^"]*)" into "([^"]*)"$`,
		func(name, version, repoKey, destination string) error {
			return w.puppetInstallWith(name, repoKey, destination, "--version", version)
		})
}

// puppetReleases writes count archives of module name, versions 1.0.0 to
// 1.<count-1>.0, under dir of the scenario's files, each named as the
// repository layout names it, <name>-<version>.tar.gz. Each holds a
// metadata.json with no dependencies and a manifest declaring the module's
// class.
func (w *world) puppetReleases(count int, name, dir string) error {
	files, err := w.filesDir()
	if err != nil {
		return err
	}
	base := filepath.Join(files, dir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	_, class, _ := strings.Cut(name, "-")
	for i := range count {
		version := fmt.Sprintf("1.%d.0", i)
		manifest, err := json.Marshal(map[string]any{
			"name": name, "version": version, "author": "acme",
			"summary": "Controlled Puppet paging module", "license": "Apache-2.0",
			"source": "https://example.invalid/fixture", "dependencies": []any{},
		})
		if err != nil {
			return err
		}
		archive, err := puppetModuleArchive(name+"-"+version, string(manifest),
			"class "+class+" { }\n")
		if err != nil {
			return err
		}
		file := filepath.Join(base, name+"-"+version+".tar.gz")
		if err := os.WriteFile(file, archive, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// puppetModuleArchive returns a module archive whose directory root holds
// manifests/init.pp and metadata.json.
func puppetModuleArchive(root, manifest, initPP string) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{
		{"manifests/init.pp", initPP}, {"metadata.json", manifest},
	} {
		name, body := f.name, f.body
		if err := tw.WriteHeader(&tar.Header{
			Name: root + "/" + name, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
