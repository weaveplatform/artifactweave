package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerVirtualTerraformProviders(sc *godog.ScenarioContext) {
	sc.Step(`^a virtual Terraform provider repository combines hosted and private remote providers$`, w.virtualTerraformProviders)
	sc.Step(`^Terraform installs both virtual providers from the "(registry|mirror)"$`, w.virtualTerraformProviderClient)
	sc.Step(`^the virtual provider bundles preserve both publishers$`, w.virtualTerraformProviderBundles)
}

func (w *world) virtualTerraformProviders() error {
	if err := w.publishNullProviderAt("registry", "tf-hosted", "local"); err != nil {
		return err
	}
	w.vars["hosted-provider-key"], w.vars["hosted-provider-signature"] = w.vars["provider-key"], w.vars["provider-signature"]
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "provider-origin", Password: "private-provider"}})
	w.closers = append(w.closers, w.remote.Close)
	w.vars["upstream"] = w.remote.URL
	if err := w.terraformProviderRemoteUpstreamAt("tf-upstream"); err != nil {
		return err
	}
	w.vars["upstream-provider-key"] = w.vars["provider-key"]
	admin := func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }
	configs := []struct {
		method, key string
		doc         any
	}{
		{
			"POST",
			"tf-upstream",
			map[string]any{
				"rclass":                "remote",
				"packageType":           "terraform",
				"url":                   w.remote.URL,
				"terraformRegistryUrl":  w.remote.URL,
				"terraformProvidersUrl": w.remote.URL,
				"username":              "provider-origin",
				"password":              "private-provider",
			},
		},
		{"PUT", "tf-modules", map[string]any{"rclass": "local", "packageType": "terraform", "terraformType": "MODULE"}},
		{"PUT", "tf-provider-inner", map[string]any{"rclass": "virtual", "packageType": "terraform", "repositories": []string{"tf-modules", "tf-hosted"}}},
		{"PUT", "tf-provider-all", map[string]any{"rclass": "virtual", "packageType": "terraform", "repositories": []string{"tf-provider-inner", "tf-upstream"}}},
	}
	for _, c := range configs {
		body, _ := json.Marshal(c.doc)
		if err := w.send(c.method, "/artifactory/api/repositories/"+c.key, string(body), admin); err != nil {
			return err
		}
		if err := w.statusIs(200); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) virtualTerraformProviderClient(mode string) error {
	host := strings.TrimPrefix(w.url, "http://")
	localSource, remoteSource := host+"/tf-provider-all-rt-ns-local/null", host+"/tf-provider-all-rt-ns-hashicorp/null"
	config, err := w.terraformRegistryServices()
	if err != nil {
		return err
	}
	if mode == "mirror" {
		if runtime.GOOS != "linux" {
			return godog.ErrSkip
		}
		if err := w.frontWithTLSBase(true); err != nil {
			return err
		}
		host = w.vars["tlshost"]
		upstream, err := url.Parse(w.remote.URL)
		if err != nil {
			return err
		}
		localSource, remoteSource = "private.example/local/null", strings.ToLower(upstream.Hostname())+"/hashicorp/null"
		// An explicit mirror-only installation rule has no direct fallback.
		config = fmt.Sprintf("provider_installation {\n network_mirror {\n url=%q\n }\n}\n", "https://"+host+"/artifactory/api/terraform/tf-provider-all/providers/")
	}
	config += fmt.Sprintf("credentials %q { token=%q }\n", host, w.vars["provider-token"])
	if err := w.writeFile(".terraformrc", &godog.DocString{Content: config}); err != nil {
		return err
	}
	w.vars["virtual-provider-local-source"], w.vars["virtual-provider-remote-source"] = localSource, remoteSource
	return w.writeFile(
		"main.tf",
		&godog.DocString{
			Content: fmt.Sprintf(
				"terraform {\n required_providers {\n hosted={source=%q,version=\"3.3.2\"}\n upstream={source=%q,version=\"3.3.2\"}\n }\n}\nresource \"null_resource\" \"hosted\" { provider=hosted }\nresource \"null_resource\" \"upstream\" { provider=upstream }\n",
				localSource,
				remoteSource,
			),
		},
	)
}

func (w *world) virtualTerraformProviderBundles() error {
	archive, err := os.ReadFile(os.Getenv("ARTIFACTWEAVE_NULL_PROVIDER_ZIP"))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(archive)
	filename := "terraform-provider-null_3.3.2_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	sums := []byte(hex.EncodeToString(digest[:]) + "  " + filename + "\n")
	for _, c := range []struct{ namespace, member, key, signature string }{
		{"local", "tf-hosted", w.vars["hosted-provider-key"], w.vars["hosted-provider-signature"]},
		{"hashicorp", "tf-upstream", w.vars["upstream-provider-key"], w.vars["remote-provider-signature"]},
	} {
		path := "/artifactory/api/terraform/tf-provider-all/v1/providers/" + c.namespace + "/null/3.3.2/download/" + runtime.GOOS + "/" + runtime.GOARCH
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
		if len(metadata.Keys.Public) != 1 || metadata.Keys.Public[0].ID != c.key {
			return fmt.Errorf("virtual provider %s used another publisher key", c.member)
		}
		for i, target := range []string{metadata.Archive, metadata.Sums, metadata.Signature} {
			if !strings.Contains(target, "/tf-provider-all/providers/") {
				return fmt.Errorf("provider URL escaped virtual: %s", c.member)
			}
			delivered, err := w.virtualProviderFile(target)
			if err != nil {
				return err
			}
			if !bytes.Equal(delivered, [][]byte{archive, sums, []byte(c.signature)}[i]) {
				return fmt.Errorf("provider %s bundle bytes changed at index %d", c.member, i)
			}
		}
	}
	for _, request := range w.remote.Requests() {
		if !request.Authorized {
			return fmt.Errorf("private provider upstream received unauthenticated %s", request.Path)
		}
	}
	for _, path := range []string{"/storage/provider.zip", "/storage/SHA256SUMS", "/storage/SHA256SUMS.sig"} {
		if w.remote.Count("GET", path) < 1 {
			return fmt.Errorf("missing private upstream file %s", path)
		}
	}
	if w.remote.Count("GET", "/registry/providers/local/null/3.3.2/download/"+runtime.GOOS+"/"+runtime.GOARCH) != 0 {
		return fmt.Errorf("hosted provider package fell through to upstream")
	}
	lock, err := os.ReadFile(w.dir + "/.terraform.lock.hcl")
	if err != nil {
		return err
	}
	for _, source := range []string{w.vars["virtual-provider-local-source"], w.vars["virtual-provider-remote-source"]} {
		if !bytes.Contains(lock, []byte(source)) {
			return fmt.Errorf("lockfile omitted provider %s", source)
		}
	}
	return nil
}

func (w *world) virtualProviderFile(target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	client := http.DefaultClient
	if request.URL.Scheme == "https" && w.tlsCA != "" {
		certificate, err := os.ReadFile(w.tlsCA)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(certificate) {
			return nil, fmt.Errorf("invalid provider fixture CA")
		}
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signed provider URL status %d", response.StatusCode)
	}
	return io.ReadAll(response.Body)
}
