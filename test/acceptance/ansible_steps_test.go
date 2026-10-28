package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/format/ansible"
)

func (w *world) registerAnsible(sc *godog.ScenarioContext) {
	sc.Step(
		`^I try Ansible (publish|install) with invalid credentials against "([^"]*)"$`,
		w.deniedAnsible,
	)
	sc.Step(`^I build the controlled Ansible collections$`, w.buildAnsible)
	sc.Step(`^I publish the controlled Ansible collections to "([^"]*)"$`, w.publishAnsible)
	sc.Step(`^I install the controlled Ansible collection from "([^"]*)"$`, w.installAnsible)
	sc.Step(`^the controlled Ansible dependency is installed$`, w.ansibleDependencyInstalled)
	sc.Step(`^the controlled Ansible Galaxy upstream is available$`, w.ansibleUpstream)
	sc.Step(`^the Ansible upstream redirects its archives$`, w.ansibleRedirects)
	sc.Step(`^the Ansible upstream corrupts its archive$`, w.ansibleCorrupt)
}

func (w *world) runAnsible(args ...string) error {
	return w.runContainer(w.ansibleImage, []string{
		"-e", "ANSIBLE_CONFIG=/work/ansible.cfg", "-e", "HOME=/work/ansible-home",
		"-e", "ANSIBLE_GALAXY_CACHE_DIR=/work/ansible-cache",
	}, append([]string{"ansible-galaxy"}, args...))
}

func (w *world) buildAnsible() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "ansible"), 0o750); err != nil {
		return err
	}
	for _, name := range []string{"dependency", "sample"} {
		target := filepath.Join(dir, "ansible", name)
		if err := os.MkdirAll(target, 0o750); err != nil {
			return err
		}
		deps := "{}"
		if name == "sample" {
			deps = "{weave.dependency: '>=1.0.0,<2.0.0'}"
		}
		metadata := fmt.Sprintf(
			"namespace: weave\nname: %s\nversion: 1.0.0\nreadme: README.md\nauthors: [ArtifactWeave]\nlicense: [MIT]\ndependencies: %s\n",
			name,
			deps,
		)
		if err := os.WriteFile(
			filepath.Join(target, "galaxy.yml"),
			[]byte(metadata),
			0o600,
		); err != nil {
			return err
		}
		if err := os.WriteFile(
			filepath.Join(target, "README.md"),
			[]byte("Controlled native Ansible acceptance collection.\n"),
			0o600,
		); err != nil {
			return err
		}
		if err := w.runAnsible(
			"collection",
			"build",
			"/work/ansible/"+name,
			"--output-path",
			"/work/ansible",
		); err != nil {
			return err
		}
		if w.lastRC != 0 {
			return fmt.Errorf("Ansible collection build failed: %s", w.body)
		}
	}
	return nil
}

func (w *world) configureAnsible(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_ANSIBLE_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	config := fmt.Sprintf(
		"[galaxy]\nserver_list=artifactweave\n[galaxy_server.artifactweave]\nurl=%s/artifactory/api/ansible/%s/\nusername=admin\npassword=%s\n",
		base,
		key,
		testAdminPassword,
	)
	return os.WriteFile(filepath.Join(dir, "ansible.cfg"), []byte(config), 0o600)
}

func (w *world) publishAnsible(key string) error {
	if err := w.configureAnsible(key); err != nil {
		return err
	}
	for _, name := range []string{"dependency", "sample"} {
		if err := w.runAnsible(
			"collection",
			"publish",
			"/work/ansible/weave-"+name+"-1.0.0.tar.gz",
			"--import-timeout",
			"30",
		); err != nil {
			return err
		}
		if w.lastRC != 0 {
			return fmt.Errorf("Ansible publish failed: %s", w.body)
		}
	}
	return nil
}

func (w *world) installAnsible(key string) error {
	if err := w.configureAnsible(key); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"ansible-installed", "ansible-cache", "ansible-home"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return w.runAnsible(
		"collection",
		"install",
		"weave.sample:==1.0.0",
		"-p",
		"/work/ansible-installed",
	)
}

func (w *world) ansibleDependencyInstalled() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"sample", "dependency"} {
		data, err := os.ReadFile(
			filepath.Join(
				dir,
				"ansible-installed",
				"ansible_collections",
				"weave",
				name,
				"MANIFEST.json",
			),
		)
		if err != nil {
			return err
		}
		var doc struct {
			CollectionInfo struct{ Name, Version string } `json:"collection_info"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return err
		}
		if doc.CollectionInfo.Name != name || doc.CollectionInfo.Version != "1.0.0" {
			return fmt.Errorf("unexpected installed collection: %s", data)
		}
	}
	return nil
}

func (w *world) ansibleUpstream() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	for _, name := range []string{"dependency", "sample"} {
		filename := "weave-" + name + "-1.0.0.tar.gz"
		data, err := os.ReadFile(filepath.Join(dir, "ansible", filename))
		if err != nil {
			return err
		}
		meta, err := ansiblefmt.ReadArchive(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		root := "/api/v3/collections/weave/" + name + "/versions/"
		doc := map[string]any{
			"namespace":    map[string]string{"name": "weave"},
			"collection":   map[string]string{"name": name},
			"version":      "1.0.0",
			"download_url": w.remote.URL + "/files/" + filename,
			"artifact":     map[string]string{"sha256": meta.SHA256},
			"metadata":     map[string]any{"dependencies": meta.Dependencies},
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		header := http.Header{"Content-Type": {"application/json"}}
		w.remote.Set(
			root,
			fakeupstream.Resource{
				Header: header,
				Body:   []byte(`{"data":[{"version":"1.0.0"}],"links":{"next":null}}`),
			},
		)
		w.remote.Set(root+"1.0.0/", fakeupstream.Resource{Header: header, Body: body})
		w.remote.Set("/files/"+filename, fakeupstream.Resource{Body: data})
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) ansibleRedirects() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"dependency", "sample"} {
		filename := "weave-" + name + "-1.0.0.tar.gz"
		data, err := os.ReadFile(filepath.Join(dir, "ansible", filename))
		if err != nil {
			return err
		}
		w.remote.Set(
			"/files/"+filename,
			fakeupstream.Resource{Redirect: w.remote.URL + "/redirected/" + filename},
		)
		w.remote.Set("/redirected/"+filename, fakeupstream.Resource{Body: data})
	}
	return nil
}

func (w *world) ansibleCorrupt() error {
	w.remote.Set(
		"/files/weave-sample-1.0.0.tar.gz",
		fakeupstream.Resource{Body: []byte("corrupted collection")},
	)
	return nil
}

func (w *world) deniedAnsible(operation, key string) error {
	if err := w.configureAnsible(key); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	config := filepath.Join(dir, "ansible.cfg")
	data, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	if err := os.WriteFile(
		config,
		bytes.ReplaceAll(data, []byte(testAdminPassword), []byte("wrong-password")),
		0o600,
	); err != nil {
		return err
	}
	for _, name := range []string{"ansible-installed", "ansible-cache", "ansible-home"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if operation == "publish" {
		return w.runAnsible(
			"collection",
			"publish",
			"/work/ansible/weave-sample-1.0.0.tar.gz",
			"--import-timeout",
			"30",
		)
	}
	return w.runAnsible(
		"collection",
		"install",
		"weave.sample:==1.0.0",
		"-p",
		"/work/ansible-installed",
	)
}
