package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

func (w *world) registerTerraformProviderRemote(sc *godog.ScenarioContext) {
	sc.Step(`^the provider API rejects the invalid upstream signature$`, func() error {
		endpoint := "/artifactory/api/terraform/tf-providers/v1/providers/hashicorp/null/3.3.2/download/" + runtime.GOOS + "/" + runtime.GOARCH
		if err := w.send("GET", endpoint, "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+w.vars["provider-token"]) }); err != nil {
			return err
		}
		if err := w.statusIs(502); err != nil {
			return err
		}
		if !strings.Contains(string(w.body), "signature") {
			return fmt.Errorf("provider error did not identify the signature failure: %s", w.body)
		}
		return nil
	})

	sc.Step(`^a remote Terraform provider upstream serves a signed null provider$`, w.terraformProviderRemoteUpstream)
	sc.Step(`^Terraform installs the remote provider from the "(registry|mirror)"$`, w.terraformProviderRemoteClient)
	sc.Step(`^the upstream provider signature is "(invalid|valid)"$`, func(state string) error {
		body := []byte(w.vars["remote-provider-signature"])
		if state == "invalid" {
			body = []byte("invalid detached signature")
		}
		w.remote.Set("/storage/SHA256SUMS.sig", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
		return nil
	})
	sc.Step(`^I forget Terraform's provider installation cache$`, func() error { return os.RemoveAll(filepath.Join(w.dir, ".terraform", "providers")) })
	sc.Step(`^the upstream provider signer and complete package were used$`, func() error {
		for _, path := range []string{"/.well-known/terraform.json", "/registry/providers/hashicorp/null/versions", "/registry/providers/hashicorp/null/3.3.2/download/" + runtime.GOOS + "/" + runtime.GOARCH, "/storage/provider.zip", "/storage/SHA256SUMS", "/storage/SHA256SUMS.sig"} {
			if w.remote.Count("GET", path) < 1 {
				return fmt.Errorf("provider upstream was not fetched: %s", path)
			}
		}
		return nil
	})
}

func (w *world) terraformProviderRemoteUpstream() error {
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	body, err := os.ReadFile(os.Getenv("ARTIFACTWEAVE_NULL_PROVIDER_ZIP"))
	if err != nil {
		return err
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		return err
	}
	rel := terraformtest.Release{Type: "null", Version: "3.3.2"}
	filename := rel.Name(runtime.GOOS + "_" + runtime.GOARCH + ".zip")
	digest := sha256.Sum256(body)
	sum := hex.EncodeToString(digest[:])
	sums := []byte(sum + "  " + filename + "\n")
	signature := key.Sign(sums)
	w.vars["remote-provider-signature"] = string(signature)
	w.vars["provider-key"] = key.KeyID()
	putJSON := func(path string, doc any) {
		raw, _ := json.Marshal(doc)
		w.remote.Set(path, fakeupstream.Resource{Body: raw, Header: http.Header{"Content-Type": {"application/json"}}})
	}
	putJSON("/.well-known/terraform.json", map[string]string{"providers.v1": "/registry/providers/"})
	platform := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	putJSON("/registry/providers/hashicorp/null/versions", map[string]any{"versions": []any{map[string]any{"version": "3.3.2", "protocols": []string{"5.0"}, "platforms": []any{platform}}}})
	putJSON("/registry/providers/hashicorp/null/3.3.2/download/"+runtime.GOOS+"/"+runtime.GOARCH, map[string]any{
		"protocols": []string{"5.0"}, "os": runtime.GOOS, "arch": runtime.GOARCH, "filename": filename,
		"download_url": "/storage/provider.zip", "shasums_url": "/storage/SHA256SUMS", "shasums_signature_url": "/storage/SHA256SUMS.sig", "shasum": sum,
		"signing_keys": map[string]any{"gpg_public_keys": []any{map[string]string{"key_id": key.KeyID(), "ascii_armor": key.Public, "source": "controlled upstream"}}},
	})
	w.remote.Set("/storage/provider.zip", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/zip"}}})
	w.remote.Set("/storage/SHA256SUMS", fakeupstream.Resource{Body: sums, Header: http.Header{"Content-Type": {"text/plain"}}})
	w.remote.Set("/storage/SHA256SUMS.sig", fakeupstream.Resource{Body: signature, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
	admin := func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }
	config, _ := json.Marshal(map[string]any{"rclass": "remote", "packageType": "terraform", "url": w.remote.URL, "terraformRegistryUrl": w.remote.URL, "terraformProvidersUrl": w.remote.URL})
	if err = w.send("PUT", "/artifactory/api/repositories/tf-providers", string(config), admin); err != nil {
		return err
	}
	if err = w.statusIs(200); err != nil {
		return err
	}
	if err = w.send("POST", "/access/api/v1/tokens", `{"expires_in":3600}`, admin); err != nil {
		return err
	}
	if err = w.statusIs(200); err != nil {
		return err
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(w.body, &token); err != nil {
		return err
	}
	w.vars["provider-token"] = token.AccessToken
	return nil
}

func (w *world) terraformProviderRemoteClient(mode string) error {
	if err := w.configureNullProvider(mode); err != nil {
		return err
	}
	if mode != "mirror" {
		return nil
	}
	upstream, err := url.Parse(w.remote.URL)
	if err != nil {
		return err
	}
	source := strings.ToLower(upstream.Hostname()) + "/hashicorp/null"
	return w.writeFile("main.tf", &godog.DocString{Content: fmt.Sprintf("terraform {\n required_providers {\n null={source=%q,version=\"3.3.2\"}\n }\n}\nresource \"null_resource\" \"proof\" {}\n", source)})
}
