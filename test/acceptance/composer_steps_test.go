package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" //nolint:gosec // Composer's dist.shasum wire field.
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerComposer(sc *godog.ScenarioContext) {
	sc.Step(`^a Composer archive with message "([^"]*)"$`, w.writeComposerArchive)
	sc.Step(`^a Composer upstream serves that archive$`, w.composerUpstream)
	sc.Step(`^a Composer project for repository "([^"]*)"$`, w.composerProject)
	sc.Step(`^I run Composer "([^"]*)"$`, w.runComposer)
	sc.Step(`^I run Composer 1 "([^"]*)"$`, w.runComposer1)
	sc.Step(`^I clear the Composer installation$`, w.clearComposerInstallation)
}

func (w *world) writeComposerArchive(message string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "composer"), 0o700); err != nil {
		return err
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, content := range map[string]string{
		"composer.json":  `{"name":"acme/widget","version":"1.0.0","type":"library"}`,
		"src/Widget.php": "<?php // " + message + "\n",
	} {
		f, e := z.Create(name)
		if e != nil {
			return e
		}
		if _, e = io.WriteString(f, content); e != nil {
			return e
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "composer", "widget.zip"), b.Bytes(), 0o600)
}

func (w *world) composerUpstream() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, "composer", "widget.zip"))
	if err != nil {
		return err
	}
	if err := w.remoteUpstream(
		"/composer/packages.json",
		`{"packages":{},"metadata-url":"/composer/p2/%package%.json"}`,
	); err != nil {
		return err
	}
	digest := sha1.Sum(archive) //nolint:gosec // Composer's dist.shasum.
	metadata := fmt.Sprintf(
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0","type":"library","dist":{"type":"zip","url":"%s/composer/dist.zip","shasum":"%x"}}]}}`,
		w.remote.URL,
		digest,
	)
	w.remote.Set("/composer/p2/acme/widget.json", fakeupstream.Resource{Body: []byte(metadata)})
	w.remote.Set(
		"/composer/dist.zip",
		fakeupstream.Resource{Redirect: w.remote.URL + "/composer/archive.zip"},
	)
	w.remote.Set("/composer/archive.zip", fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) composerProject(repoKey string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	project := filepath.Join(dir, "composer-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		return err
	}
	doc := map[string]any{
		"name":    "acceptance/root",
		"require": map[string]string{"acme/widget": "1.0.0"},
		"repositories": []any{
			map[string]string{
				"type": "composer",
				"url":  w.url + "/artifactory/api/composer/" + repoKey,
			},
			map[string]bool{"packagist.org": false},
		},
		"config": map[string]any{"secure-http": false, "allow-plugins": false},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(project, "composer.json"), data, 0o600)
}

func (w *world) runComposer(args string) error  { return w.runComposerClient("composer", args) }
func (w *world) runComposer1(args string) error { return w.runComposerClient("composer1", args) }
func (w *world) runComposerClient(binary, args string) error {
	endpoint := strings.TrimPrefix(w.url, "http://")
	authJSON, err := json.Marshal(
		map[string]any{
			"http-basic": map[string]any{
				endpoint: map[string]string{"username": "admin", "password": testAdminPassword},
			},
		},
	)
	if err != nil {
		return err
	}
	flags := []string{
		"--entrypoint",
		"/usr/local/bin/" + binary,
		"-w",
		"/work/composer-project",
		"-e",
		"COMPOSER_AUTH=" + string(authJSON),
		"-e",
		"COMPOSER_CACHE_DIR=/work/composer-cache",
	}
	return w.runContainer(w.composerImage, flags, w.clientArgs(args))
}

func (w *world) clearComposerInstallation() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, path := range []string{"composer-project/vendor", "composer-project/composer.lock", "composer-cache"} {
		if err := os.RemoveAll(filepath.Join(dir, path)); err != nil {
			return err
		}
	}
	return nil
}
