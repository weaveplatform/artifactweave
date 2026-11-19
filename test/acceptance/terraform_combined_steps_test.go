package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

const combinedModule = "output \"release\" { value = \"combined virtual 1.0.0\" }\n"

func (w *world) registerCombinedTerraform(sc *godog.ScenarioContext) {
	sc.Step(`^one Terraform virtual combines a local module and a private remote provider$`, w.combinedTerraform)
	sc.Step(`^I clear the combined Terraform installation and plugin caches$`, w.clearCombinedTerraform)
	sc.Step(`^I run the combined Terraform "([^"]*)"$`, w.runCombinedTerraform)
	sc.Step(`^this init fetched the module and signed provider only through the combined virtual$`, w.combinedTerraformRequests)
	sc.Step(`^the combined Terraform installation preserves module and publisher bytes$`, w.combinedTerraformInstallation)
	sc.Step(`^the applied provider resource uses the combined module output$`, w.combinedTerraformState)
}

func (w *world) combinedTerraform() error {
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "provider-origin", Password: "private-provider"}})
	w.closers = append(w.closers, w.remote.Close)
	w.vars["upstream"] = w.remote.URL
	if err := w.terraformProviderRemoteUpstreamAt("tf-combined-upstream"); err != nil {
		return err
	}
	admin := func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }
	for _, c := range []struct {
		method, key string
		config      any
	}{
		{"POST", "tf-combined-upstream", map[string]any{
			"rclass": "remote", "packageType": "terraform", "url": w.remote.URL,
			"terraformRegistryUrl": w.remote.URL, "terraformProvidersUrl": w.remote.URL,
			"username": "provider-origin", "password": "private-provider",
		}},
		{"PUT", "tf-combined-modules", map[string]any{"rclass": "local", "packageType": "terraform", "terraformType": "MODULE"}},
		{"PUT", "tf-combined", map[string]any{"rclass": "virtual", "packageType": "terraform", "repositories": []string{"tf-combined-modules", "tf-combined-upstream"}}},
	} {
		body, err := json.Marshal(c.config)
		if err != nil {
			return err
		}
		if err = w.send(c.method, "/artifactory/api/repositories/"+c.key, string(body), admin); err != nil {
			return err
		}
		if err = w.statusIs(200); err != nil {
			return err
		}
	}
	if err := w.writeZip("module.zip", "main.tf", combinedModule); err != nil {
		return err
	}
	if err := w.deployFile("module.zip", "/artifactory/tf-combined-modules/acme/greeting/aws/1.0.0.zip"); err != nil {
		return err
	}
	if err := w.statusIs(201); err != nil {
		return err
	}
	config, err := w.terraformRegistryServices()
	if err != nil {
		return err
	}
	host := strings.TrimPrefix(w.url, "http://")
	source := host + "/tf-combined-rt-ns-hashicorp/null"
	w.vars["combined-provider-source"] = source
	// Restrict provider installation to this address: an implicit public null
	// provider or direct member source must fail instead of rescuing the proof.
	config += fmt.Sprintf("credentials %q { token=%q }\nprovider_installation {\n direct { include=[%q] }\n}\n", host, w.vars["provider-token"], source)
	if err = w.writeFile(".terraformrc", &godog.DocString{Content: config}); err != nil {
		return err
	}
	return w.writeFile("main.tf", &godog.DocString{Content: fmt.Sprintf(`terraform {
  required_providers {
    null = { source = %q, version = "3.3.2" }
  }
}
module "greeting" {
  source = %q
  version = "1.0.0"
}
resource "null_resource" "proof" {
  triggers = { release = module.greeting.release }
}
output "release" { value = null_resource.proof.triggers.release }
`, source, host+"/tf-combined__acme/greeting/aws")})
}

func (w *world) clearCombinedTerraform() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	// Both paths are constants inside this scenario's owned directory. Keep
	// state and lockfile so the repeat proves the same selection and resource.
	for _, name := range []string{".terraform", "combined-plugin-cache"} {
		if err = os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	cache := filepath.Join(dir, "combined-plugin-cache")
	if err = os.Mkdir(cache, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("combined plugin cache is not empty")
	}
	_, err = os.Stat(filepath.Join(dir, ".terraform"))
	if !os.IsNotExist(err) {
		return fmt.Errorf("combined installation remains after clearing: %v", err)
	}
	return nil
}

func (w *world) runCombinedTerraform(args string) error {
	return w.runTerraformWithEnv(args, []string{"TF_PLUGIN_CACHE_DIR=" + filepath.Join(w.dir, "combined-plugin-cache")})
}

func (w *world) combinedTerraformRequests() error {
	if !bytes.Contains(w.body, []byte(w.vars["provider-key"])) {
		return fmt.Errorf("native init did not report the private origin's publisher key")
	}
	if err := w.awaitLoggedSoFar(); err != nil {
		return err
	}
	base := "/artifactory/api/terraform/"
	paths := map[string]int{
		base + "v1/modules/tf-combined__acme/greeting/aws/versions":                                                  200,
		base + "v1/modules/tf-combined__acme/greeting/aws/1.0.0/download":                                            204,
		base + "tf-combined/v1/modules/acme/greeting/aws/1.0.0.zip":                                                  200,
		base + "v1/providers/tf-combined-rt-ns-hashicorp/null/versions":                                              200,
		base + "v1/providers/tf-combined-rt-ns-hashicorp/null/3.3.2/download/" + runtime.GOOS + "/" + runtime.GOARCH: 200,
		base + "tf-combined/providers/storage/provider.zip":                                                          200,
		base + "tf-combined/providers/storage/SHA256SUMS":                                                            200,
		base + "tf-combined/providers/storage/SHA256SUMS.sig":                                                        200,
	}
	seen := map[string]bool{}
	// Audit before the separate byte/signature probes: only this native init's
	// own requests can satisfy the required discovery and download evidence.
	for line := range strings.Lines(w.stderr.String()[w.commandLog:]) {
		var record struct {
			Msg, Method, Path string
			Status            int
		}
		if json.Unmarshal([]byte(line), &record) != nil || record.Msg != "request" {
			continue
		}
		path, _, _ := strings.Cut(record.Path, "?")
		if path == "/artifactory/api/system/ping" {
			continue
		}
		// Go-getter probes the module ZIP with HEAD before downloading it.
		// Permit that probe without letting it satisfy the required GET.
		probe := record.Method == "HEAD" && strings.HasSuffix(path, ".zip")
		if want, ok := paths[path]; !ok || (record.Method != "GET" && !probe) || record.Status != want {
			return fmt.Errorf("combined init used an unexpected route: %s %s (%d)", record.Method, path, record.Status)
		}
		if record.Method == "GET" {
			seen[path] = true
		}
	}
	for path := range paths {
		if !seen[path] {
			return fmt.Errorf("this init did not fetch %s through the combined virtual", path)
		}
	}
	for _, request := range w.remote.Requests() {
		if !request.Authorized {
			return fmt.Errorf("private origin received unauthenticated %s", request.Path)
		}
	}
	return nil
}

func (w *world) combinedTerraformInstallation() error {
	if err := w.terraformModuleVersion("greeting", "1.0.0", "."); err != nil {
		return err
	}
	module, err := os.ReadFile(filepath.Join(w.dir, ".terraform", "modules", "greeting", "main.tf"))
	if err != nil {
		return err
	}
	if string(module) != combinedModule {
		return fmt.Errorf("combined module bytes changed: %q", module)
	}
	archive, err := os.ReadFile(os.Getenv("ARTIFACTWEAVE_NULL_PROVIDER_ZIP"))
	if err != nil {
		return err
	}
	if err = w.combinedProviderBinary(archive); err != nil {
		return err
	}
	return w.combinedProviderBundle(archive)
}

func (w *world) combinedProviderBinary(archive []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if !strings.HasPrefix(file.Name, "terraform-provider-null_") {
			continue
		}
		body, err := file.Open()
		if err != nil {
			return err
		}
		expected, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			return err
		}
		for _, root := range []string{filepath.Join(".terraform", "providers"), "combined-plugin-cache"} {
			path := filepath.Join(w.dir, root, filepath.FromSlash(w.vars["combined-provider-source"]), "3.3.2", runtime.GOOS+"_"+runtime.GOARCH, file.Name)
			installed, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(installed, expected) {
				return fmt.Errorf("combined provider binary differs from pinned ZIP in %s", root)
			}
		}
		return nil
	}
	return fmt.Errorf("pinned ZIP contains no null provider executable")
}

func (w *world) combinedProviderBundle(archive []byte) error {
	path := "/artifactory/api/terraform/tf-combined/v1/providers/hashicorp/null/3.3.2/download/" + runtime.GOOS + "/" + runtime.GOARCH
	if err := w.send("GET", path, "", func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }); err != nil {
		return err
	}
	if err := w.statusIs(200); err != nil {
		return err
	}
	var metadata struct {
		Archive   string `json:"download_url"`
		Sums      string `json:"shasums_url"`
		Signature string `json:"shasums_signature_url"`
		Keys      struct {
			Public []struct {
				ID string `json:"key_id"`
			} `json:"gpg_public_keys"`
		} `json:"signing_keys"`
	}
	if err := json.Unmarshal(w.body, &metadata); err != nil {
		return err
	}
	if len(metadata.Keys.Public) != 1 || metadata.Keys.Public[0].ID != w.vars["provider-key"] {
		return fmt.Errorf("combined virtual changed the upstream publisher")
	}
	digest := sha256.Sum256(archive)
	sums := hex.EncodeToString(digest[:]) + "  terraform-provider-null_3.3.2_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip\n"
	for i, target := range []string{metadata.Archive, metadata.Sums, metadata.Signature} {
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		if u.Scheme+"://"+u.Host != w.url || !strings.HasPrefix(u.Path, "/artifactory/api/terraform/tf-combined/providers/") {
			return fmt.Errorf("combined provider URL escaped the virtual: %s", u.Path)
		}
		delivered, err := w.virtualProviderFile(target)
		if err != nil {
			return err
		}
		if !bytes.Equal(delivered, [][]byte{archive, []byte(sums), []byte(w.vars["remote-provider-signature"])}[i]) {
			return fmt.Errorf("combined provider bundle bytes changed at index %d", i)
		}
	}
	lock, err := os.ReadFile(filepath.Join(w.dir, ".terraform.lock.hcl"))
	if err != nil {
		return err
	}
	if !bytes.Contains(lock, []byte(w.vars["combined-provider-source"])) || !bytes.Contains(lock, []byte("h1:")) {
		return fmt.Errorf("combined provider lockfile lost the virtual source or integrity hash")
	}
	return nil
}

func (w *world) combinedTerraformState() error {
	body, err := os.ReadFile(filepath.Join(w.dir, "terraform.tfstate"))
	if err != nil {
		return err
	}
	var state struct {
		Outputs   map[string]struct{ Value string }
		Resources []struct {
			Type, Name, Provider string
			Instances            []struct {
				Attributes struct {
					ID       string
					Triggers map[string]string
				}
			}
		}
	}
	if err = json.Unmarshal(body, &state); err != nil {
		return err
	}
	if len(state.Resources) != 1 || len(state.Resources[0].Instances) != 1 {
		return fmt.Errorf("combined apply did not create exactly one provider resource")
	}
	resource := state.Resources[0]
	attributes := resource.Instances[0].Attributes
	if resource.Type != "null_resource" || resource.Name != "proof" || !strings.Contains(resource.Provider, w.vars["combined-provider-source"]) || attributes.ID == "" ||
		attributes.Triggers["release"] != "combined virtual 1.0.0" ||
		state.Outputs["release"].Value != attributes.Triggers["release"] {
		return fmt.Errorf("applied provider resource did not preserve the virtual module output")
	}
	if previous := w.vars["combined-resource-id"]; previous != "" && previous != attributes.ID {
		return fmt.Errorf("cache clearing replaced the existing provider resource")
	}
	w.vars["combined-resource-id"] = attributes.ID
	return nil
}
