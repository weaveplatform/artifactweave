package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/cucumber/godog"
	"github.com/go-chi/chi/v5"
)

func (w *world) registerChef(sc *godog.ScenarioContext) {
	sc.Step(`^the Chef cookbook sources are ready$`, w.chefSources)
	sc.Step(`^I publish the Chef cookbooks to "([^"]*)"$`, w.publishChef)
	sc.Step(`^I publish Chef cookbook "([^"]*)" to "([^"]*)"$`, w.publishChefCookbook)
	sc.Step(`^the Chef upstream metadata was refreshed$`, func() error {
		if w.chefMetadataReads.Load() < 8 {
			return fmt.Errorf(
				"expected refreshed Chef metadata, got %d requests",
				w.chefMetadataReads.Load(),
			)
		}
		return nil
	})
	sc.Step(`^I install Chef cookbook "([^"]*)" from "([^"]*)" into "([^"]*)"$`, w.installChef)
	sc.Step(`^I unshare Chef cookbook "([^"]*)" version "([^"]*)" from "([^"]*)"$`, w.unshareChef)
	sc.Step(`^a Chef upstream serves the cookbooks published in "([^"]*)"$`, w.chefUpstream)
	sc.Step(`^the Chef upstream stops$`, func() { w.chefUp.Close() })
}

func (w *world) chefSources() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"helper", "widget"} {
		target := filepath.Join(dir, "chef-source", name)
		if err = os.MkdirAll(filepath.Join(target, "recipes"), 0o750); err != nil {
			return err
		}
		data := fmt.Sprintf(
			"name %q\nversion '1.0.0'\nlicense 'MIT'\nmaintainer 'ArtifactWeave'\ndescription 'Controlled native Chef fixture'\n",
			name,
		)
		if name == "widget" {
			data += "depends 'helper', '>= 1.0.0'\n"
		}
		if err = os.WriteFile(
			filepath.Join(target, "metadata.rb"),
			[]byte(data),
			0o600,
		); err != nil {
			return err
		}
		if err = os.WriteFile(
			filepath.Join(target, "recipes", "default.rb"),
			[]byte("# Controlled acceptance recipe.\n"),
			0o600,
		); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) chefConfig(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	endpoint := w.url
	if os.Getenv("ARTIFACTWEAVE_CHEF_DESKTOP") == "1" {
		endpoint = strings.Replace(endpoint, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	u.User = url.UserPassword("admin", testAdminPassword)
	u.Path = "/artifactory/api/chef/" + key
	config := fmt.Sprintf(
		"knife[:supermarket_site] = %q\ncookbook_path ['/work/chef-source']\nnode_name 'admin'\n",
		u.String(),
	)
	return os.WriteFile(filepath.Join(dir, "chef.rb"), []byte(config), 0o600)
}

func (w *world) runChef(args ...string) error {
	return w.runContainer(
		w.chefImage,
		[]string{"-e", "HOME=/work/chef-home", "-e", "CHEF_LICENSE=accept-no-persist"},
		append(args, "--config", "/work/chef.rb"),
	)
}

func (w *world) publishChef(key string) error {
	if err := w.chefConfig(key); err != nil {
		return err
	}
	for _, name := range []string{"helper", "widget"} {
		if err := w.runChef("artifactory", "share", name, "Other"); err != nil {
			return err
		}
		if w.lastRC != 0 {
			return fmt.Errorf("Chef publish: %s", w.body)
		}
	}
	return nil
}

func (w *world) publishChefCookbook(name, key string) error {
	if err := w.chefConfig(key); err != nil {
		return err
	}
	if err := w.runChef("artifactory", "share", name, "Other"); err != nil {
		return err
	}
	if w.lastRC != 0 {
		return fmt.Errorf("Chef publish: %s", w.body)
	}
	return nil
}

func (w *world) installChef(name, key, destination string) error {
	if err := w.chefConfig(key); err != nil {
		return err
	}
	if err := w.runContainer(
		w.chefImage,
		[]string{"--entrypoint", "sh"},
		[]string{
			"-c",
			`mkdir -p "$1" && git -C "$1" init -b master && git -C "$1" config user.email acceptance@example.invalid && git -C "$1" config user.name Acceptance && git -C "$1" commit --allow-empty -m initial`,
			"chef-git",
			"/work/" + destination,
		},
	); err != nil {
		return err
	}
	if w.lastRC != 0 {
		return fmt.Errorf("Chef installation repository: %s", w.body)
	}
	return w.runChef(
		"artifactory",
		"install",
		name,
		"1.0.0",
		"--cookbook-path",
		"/work/"+destination,
	)
}

func (w *world) unshareChef(name, version, key string) error {
	if err := w.chefConfig(key); err != nil {
		return err
	}
	return w.runChef("artifactory", "unshare", name, version, "--yes")
}

func (w *world) chefUpstream(key string) error {
	resources := map[string][]byte{}
	w.chefMetadataReads = &atomic.Int64{}
	for _, name := range []string{"helper", "widget"} {
		req, err := http.NewRequest(
			http.MethodGet,
			w.url+"/artifactory/"+key+"/"+name+"/"+name+"-1.0.0.tar.gz",
			nil,
		)
		if err != nil {
			return err
		}
		req.SetBasicAuth("admin", testAdminPassword)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("Chef seed archive: HTTP %d", resp.StatusCode)
		}
		resources[name] = data
	}
	r := chi.NewRouter()
	var up *httptest.Server
	r.Get("/chef/api/v1/cookbooks/{name}", func(out http.ResponseWriter, req *http.Request) {
		w.chefMetadataReads.Add(1)
		name := chi.URLParam(req, "name")
		if _, ok := resources[name]; !ok {
			http.NotFound(out, req)
			return
		}
		out.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(out).
			Encode(map[string]any{"name": name, "latest_version": up.URL + "/chef/api/v1/cookbooks/" + name + "/versions/1_0_0", "versions": []string{up.URL + "/chef/api/v1/cookbooks/" + name + "/versions/1_0_0"}})
	})
	r.Get(
		"/chef/api/v1/cookbooks/{name}/versions/1_0_0",
		func(out http.ResponseWriter, req *http.Request) {
			w.chefMetadataReads.Add(1)
			name := chi.URLParam(req, "name")
			data, ok := resources[name]
			if !ok {
				http.NotFound(out, req)
				return
			}
			sum := sha256.Sum256(data)
			deps := map[string]string{}
			if name == "widget" {
				deps["helper"] = ">= 1.0.0"
			}
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).
				Encode(map[string]any{"version": "1.0.0", "file": up.URL + "/chef/download/" + name, "cookbook": up.URL + "/chef/api/v1/cookbooks/" + name, "tarball_file_size": len(data), "sha256": hex.EncodeToString(sum[:]), "dependencies": deps, "platforms": map[string]string{}})
		},
	)
	r.Get("/chef/download/{name}", func(out http.ResponseWriter, req *http.Request) {
		http.Redirect(out, req, "/chef/blobs/"+chi.URLParam(req, "name"), http.StatusFound)
	})
	r.Get("/chef/blobs/{name}", func(out http.ResponseWriter, req *http.Request) {
		data, ok := resources[chi.URLParam(req, "name")]
		if !ok {
			http.NotFound(out, req)
			return
		}
		out.Header().Set("Content-Type", "application/octet-stream")
		_, _ = out.Write(data)
	})
	up = httptest.NewServer(r)
	w.chefUp = up
	w.closers = append(w.closers, up.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = up.URL
	return nil
}
