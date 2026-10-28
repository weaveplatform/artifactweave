package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func (w *world) registerPuppet(sc *godog.ScenarioContext) {
	sc.Step(`^the Puppet upstream stops$`, func() { w.puppetUp.Close() })
	sc.Step(`^the Puppet fixtures are ready$`, w.puppetFixtures)
	sc.Step(`^I install Puppet module "([^"]*)" from "([^"]*)" into "([^"]*)"$`, w.puppetInstall)
	sc.Step(`^a Puppet upstream serves the controlled modules$`, w.puppetUpstream)
}

func (w *world) puppetFixtures() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"widget", "helper"} {
		file := "acme-" + name + "-1.0.0.tar.gz"
		data, err := os.ReadFile(filepath.Join("fixtures", "puppet", file))
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) puppetInstall(name, repoKey, destination string) error {
	if err := w.clientReachable("Puppet"); err != nil {
		return err
	}
	root := w.url
	if os.Getenv("ARTIFACTWEAVE_PUPPET_DESKTOP") == "1" {
		root = strings.Replace(root, "127.0.0.1", "host.docker.internal", 1)
	}
	args := []string{
		"module",
		"install",
		name,
		"--module_repository",
		root + "/artifactory/api/puppet/" + repoKey,
		"--target-dir",
		"/work/" + destination,
		"--modulepath",
		"/work/" + destination,
		"--vardir",
		"/work/.puppet-var-" + destination,
		"--confdir",
		"/work/.puppet-conf",
		"--forge_authorization",
		"Bearer " + w.vars["puppet-token"],
	}
	return w.runContainer(w.puppetImage, nil, args)
}

func (w *world) puppetUpstream() error {
	resources := map[string][]byte{}
	for _, name := range []string{"widget", "helper"} {
		file := "acme-" + name + "-1.0.0.tar.gz"
		data, err := os.ReadFile(filepath.Join("fixtures", "puppet", file))
		if err != nil {
			return err
		}
		manifest, err := os.ReadFile(filepath.Join("fixtures", "puppet", name, "metadata.json"))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		doc := map[string]any{
			"results": []any{
				map[string]any{
					"version":     "1.0.0",
					"file_uri":    "/puppet/v3/files/" + file,
					"file_sha256": hex.EncodeToString(sum[:]),
					"file_size":   len(data),
					"metadata":    json.RawMessage(manifest),
				},
			},
			"pagination": map[string]any{"offset": 0, "total": 1, "next": nil},
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		resources["acme-"+name] = body
		resources["/puppet/blobs/"+file] = data
	}
	up := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if key == "/puppet/v3/releases" {
			key = r.URL.Query().Get("module")
			out.Header().Set("Content-Type", "application/json")
		}
		if strings.HasPrefix(key, "/puppet/v3/files/") {
			http.Redirect(
				out,
				r,
				strings.Replace(key, "/v3/files/", "/blobs/", 1),
				http.StatusFound,
			)
			return
		}
		data, ok := resources[key]
		if !ok {
			http.NotFound(out, r)
			return
		}
		_, _ = out.Write(data)
	}))
	w.puppetUp = up
	w.closers = append(w.closers, up.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = up.URL
	return nil
}
