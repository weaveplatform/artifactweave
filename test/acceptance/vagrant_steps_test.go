package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"
)

func (w *world) registerVagrant(sc *godog.ScenarioContext) {
	sc.Step(`^a Vagrant box "([^"]*)" for provider "([^"]*)" containing "([^"]*)"$`, w.vagrantBox)
	sc.Step(`^a Vagrant client for repository "([^"]*)" using token "([^"]*)"$`, w.vagrantClient)
	sc.Step(`^I run vagrant "([^"]*)"$`, w.runVagrant)
	sc.Step(`^the installed Vagrant boxes contain "([^"]*)"$`, w.vagrantInstalledContent)
}

// These controlled boxes exercise registry transfer and checksum verification;
// fixture.txt stands in for a VM disk. No VM is booted by registry acceptance.
func (w *world) vagrantBox(name, provider, content string) error {
	metadata, err := json.Marshal(map[string]string{"provider": provider, "architecture": "amd64"})
	if err != nil {
		return err
	}
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct{ name, body string }{
		{"metadata.json", string(metadata)},
		{"Vagrantfile", "Vagrant.configure(\"2\") do |config|\nend\n"},
		{"fixture.txt", content},
	} {
		if err := tw.WriteHeader(
			&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body))},
		); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data.Bytes(), 0o600)
}

func (w *world) vagrantClient(key, token string) error {
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_VAGRANT_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	w.vars["vagrant-url"] = base + "/artifactory/api/vagrant/" + key
	w.vars["vagrant-auth"] = w.expand(token)
	return nil
}

func (w *world) runVagrant(args string) error {
	return w.runContainer(w.vagrantImage, []string{
		"--platform", "linux/amd64",
		"-e", "VAGRANT_HOME=/work/.vagrant.d",
		"-e", "VAGRANT_SERVER_URL=" + w.vars["vagrant-url"],
		"-e", "ATLAS_TOKEN=" + w.vars["vagrant-auth"],
	}, append([]string{"vagrant", "--no-color"}, w.clientArgs(args)...))
}

func (w *world) vagrantInstalledContent(want string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	found := false
	err = filepath.WalkDir(
		filepath.Join(dir, ".vagrant.d", "boxes"),
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "fixture.txt" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			found = found || string(data) == want
			return nil
		},
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("installed Vagrant boxes do not contain expected fixture bytes %q", want)
	}
	return nil
}
