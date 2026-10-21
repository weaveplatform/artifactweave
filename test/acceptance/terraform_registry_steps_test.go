package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func (w *world) registerTerraformRegistry(sc *godog.ScenarioContext) {
	sc.Step(`^Terraform uses the discovered registry services$`, func() error {
		config, err := w.terraformRegistryServices()
		if err != nil {
			return err
		}
		return w.writeFile(".terraformrc", &godog.DocString{Content: config})
	})
	sc.Step(`^I run terraform "([^"]*)" with the kept environment token "([^"]*)"$`,
		func(args, token string) error {
			host := strings.TrimPrefix(w.url, "http://")
			return w.runTerraformWithEnv(args, []string{
				"TF_TOKEN_" + strings.ReplaceAll(host, ".", "_") + "=" + w.vars[token],
			})
		})
	sc.Step(
		`^Terraform selected module "([^"]*)" at version "([^"]*)" in "([^"]*)"$`,
		w.terraformModuleVersion,
	)
	sc.Step(`^I forget Terraform's module cache$`, func() error {
		dir, err := w.filesDir()
		if err != nil {
			return err
		}
		// Keep the provider cache; only module selection must start afresh
		// after a REST delete or restore of a module archive.
		return os.RemoveAll(filepath.Join(dir, ".terraform", "modules"))
	})
}

// Terraform's discovery request requires HTTPS, but the returned service URLs
// may use HTTP. Copy the real document into an isolated HOME's default config:
// TF_CLI_CONFIG_FILE would prevent loading credentials.tfrc.json alongside it.
func (w *world) terraformRegistryServices() (string, error) {
	if err := w.get("/.well-known/terraform.json"); err != nil {
		return "", err
	}
	if err := w.statusIs(200); err != nil {
		return "", err
	}
	var services map[string]string
	if err := json.Unmarshal(w.body, &services); err != nil {
		return "", err
	}
	var config strings.Builder
	fmt.Fprintf(&config, "host %q {\n services = {\n", strings.TrimPrefix(w.url, "http://"))
	for _, name := range []string{"modules.v1", "providers.v1"} {
		if services[name] == "" {
			return "", fmt.Errorf("discovery has no %s service", name)
		}
		fmt.Fprintf(&config, " %q = %q\n", name, services[name])
	}
	config.WriteString(" }\n}\n")
	return config.String(), nil
}

func (w *world) terraformModuleVersion(name, version, root string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, root, ".terraform", "modules", "modules.json"))
	if err != nil {
		return err
	}
	var manifest struct {
		Modules []struct {
			Key     string
			Version string
		}
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		return err
	}
	for _, module := range manifest.Modules {
		if module.Key == name {
			if module.Version != version {
				return fmt.Errorf("module %s selected %s, want %s", name, module.Version, version)
			}
			return nil
		}
	}
	return fmt.Errorf("module %s missing from %s", name, b)
}
