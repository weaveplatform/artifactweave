package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

func (w *world) registerTerraformProviders(sc *godog.ScenarioContext) {
	sc.Step(`^the pinned null provider is available$`, func() error {
		if os.Getenv("ARTIFACTWEAVE_NULL_PROVIDER_ZIP") == "" {
			return godog.ErrSkip
		}
		return nil
	})
	sc.Step(`^a null provider is published for the "(registry|mirror)"$`, w.publishNullProvider)
	sc.Step(`^Terraform installs from the "(registry|mirror)"$`, w.configureNullProvider)
}

func (w *world) publishNullProvider(mode string) error {
	archive, err := os.ReadFile(os.Getenv("ARTIFACTWEAVE_NULL_PROVIDER_ZIP"))
	if err != nil {
		return err
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		return err
	}
	admin := func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }
	check := func(method, path, body string, want int) error {
		if err := w.send(method, path, body, admin); err != nil {
			return err
		}
		return w.statusIs(want)
	}
	config := map[string]string{
		"rclass":        "local",
		"packageType":   "terraform",
		"terraformType": "PROVIDER",
	}
	if mode == "registry" {
		body, _ := json.Marshal(
			map[string]string{
				"alias": "tf-sign", "pairName": "tf-sign",
				"pairType":   "GPG",
				"publicKey":  key.Public,
				"privateKey": key.Private,
			},
		)
		if err = check("POST", "/artifactory/api/security/keypair", string(body), 201); err != nil {
			return err
		}
		config["primaryKeyPairRef"] = "tf-sign"
	}
	body, _ := json.Marshal(config)
	if err = check(
		"PUT",
		"/artifactory/api/repositories/tf-providers",
		string(body),
		200,
	); err != nil {
		return err
	}
	rel := terraformtest.Release{Type: "null", Version: "3.3.2"}
	name := rel.Name(runtime.GOOS + "_" + runtime.GOARCH + ".zip")
	files := map[string][]byte{name: archive}
	if mode == "registry" {
		sum := sha256.Sum256(archive)
		sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
		files[rel.Name("SHA256SUMS")] = sums
		files[rel.Name("SHA256SUMS.sig")] = key.Sign(sums)
	}
	for name, b := range files {
		if err = check(
			"PUT",
			"/artifactory/tf-providers/hashicorp/null/3.3.2/"+name,
			string(b),
			201,
		); err != nil {
			return err
		}
	}
	if err = check("POST", "/access/api/v1/tokens", `{"expires_in":3600}`, 200); err != nil {
		return err
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(w.body, &token); err != nil {
		return err
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["provider-token"] = token.AccessToken
	w.vars["provider-key"] = key.KeyID()
	return nil
}

func (w *world) configureNullProvider(mode string) error {
	host := strings.TrimPrefix(w.url, "http://")
	source := host + "/tf-providers-rt-ns-hashicorp/null"
	config, err := w.terraformRegistryServices()
	if err != nil {
		return err
	}
	if mode == "mirror" {
		// Terraform1.16.4 uses Go1.26: SSL_CERT_FILE is Linux-only. CI runs this
		// feature on Linux; local macOS/Windows runs explicitly skip it.
		if runtime.GOOS != "linux" {
			return godog.ErrSkip
		}
		if err := w.frontWithTLSBase(true); err != nil {
			return err
		}
		host = w.vars["tlshost"]
		source = "private.example/hashicorp/null"
		config = fmt.Sprintf(
			"provider_installation {\n network_mirror {\n url=%q\n }\n}\n",
			"https://"+host+"/artifactory/api/terraform/tf-providers/providers/",
		)
	}
	config += fmt.Sprintf("credentials %q { token=%q }\n", host, w.vars["provider-token"])
	if err := w.writeFile(".terraformrc", &godog.DocString{Content: config}); err != nil {
		return err
	}
	return w.writeFile(
		"main.tf",
		&godog.DocString{
			Content: fmt.Sprintf(
				"terraform {\nrequired_providers {\nnull={source=%q,version=\"3.3.2\"}\n}\n}\nresource \"null_resource\" \"proof\" {}\n",
				source,
			),
		},
	)
}
