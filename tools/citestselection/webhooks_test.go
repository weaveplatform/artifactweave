package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitWebhookProviderInventoryIsRequired(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(root, "test/terraform/check-webhooks.py")
	if out, err := exec.Command("python3", "-I", checker, "--inventory", root).CombinedOutput(); err != nil {
		t.Fatalf("required 24-resource inventory: %v: %s", err, out)
	}
	runner, err := os.ReadFile(filepath.Join(root, "test/terraform/run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{
		`python3 -I "$here/check-webhooks.py" --inventory "$root"`,
		`deploy/artifactweave/terraform/webhooks) apply_webhooks ;;`,
		`python3 -I "$here/check-webhooks.py" --deleted "$work/webhook-keys.json"`,
	} {
		if !strings.Contains(string(runner), call) {
			t.Errorf("provider runner lost required webhook step %s", call)
		}
	}
	for _, path := range []string{
		"test/terraform/webhook-cases.json", "test/terraform/check-webhooks.py",
		"deploy/artifactweave/terraform/webhooks/main.tf",
	} {
		provider, _ := terraformPathLanes(path)
		if !provider {
			t.Errorf("%s must select provider conformance", path)
		}
	}
}
