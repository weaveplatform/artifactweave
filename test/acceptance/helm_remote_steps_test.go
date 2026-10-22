package acceptance

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerRemoteHelm(sc *godog.ScenarioContext) {
	sc.Step(`^a Helm upstream serves the files under "([^"]*)"$`, w.helmUpstream)
	sc.Step(`^the Helm client metadata cache is cleared$`, w.helmClearCache)
	sc.Step(
		`^the Helm provenance "([^"]*)" has a tampered signed description$`,
		w.helmTamperProvenance,
	)
}

func (w *world) helmTamperProvenance(name string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	before := "description: A Helm chart for Kubernetes"
	if !strings.Contains(string(data), before) {
		return fmt.Errorf("provenance does not contain fixture description")
	}
	data = []byte(strings.Replace(string(data), before, "description: tampered signed metadata", 1))
	return os.WriteFile(path, data, 0o600)
}

func (w *world) helmUpstream(name string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err = w.remoteUpstream("/health", "up"); err != nil {
		return err
	}
	root := filepath.Join(dir, name)
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		media := "application/octet-stream"
		if rel == "index.yaml" {
			media = "application/x-yaml"
		}
		w.remote.Set(
			"/"+filepath.ToSlash(rel),
			fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": []string{media}}},
		)
		return nil
	})
}

func (w *world) helmClearCache() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err = os.RemoveAll(filepath.Join(dir, ".cache", "helm")); err != nil {
		return err
	}
	for _, name := range []string{"acme-0.1.0.tgz", "acme-0.1.0.tgz.prov"} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("remove client download: %w", err)
		}
	}
	return nil
}
