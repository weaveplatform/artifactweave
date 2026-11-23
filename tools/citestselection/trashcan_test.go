package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The trashcan reference is the pinned-provider qualification of the
// descriptor's trashcanConfig (#1829). Dropping its scenario, handler or
// effect checks would leave a provider-visible section unqualified while CI
// stays green, so each is required here.
func TestUnitTrashcanProviderScenarioIsRequired(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	read := func(file string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		// Windows checkouts may convert to CRLF; the anchors below match lines.
		return strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	const scenario = "deploy/artifactweave/terraform/trashcan"
	if !strings.Contains(read("Makefile"), "\t"+scenario) {
		t.Fatal("trashcan scenario left the required provider scenarios")
	}
	if strings.Count(read(scenario+"/main.tf"), `resource "artifactory_trashcan_config"`) != 1 {
		t.Fatal("trashcan scenario must manage one artifactory_trashcan_config")
	}
	if !strings.Contains(read(scenario+"/.terraform.lock.hcl"), `version     = "12.11.14"`) ||
		!strings.Contains(read(scenario+"/versions.tf"), `version = "12.11.14"`) {
		t.Fatal("trashcan scenario lost the pinned provider")
	}
	runner := read("test/terraform/run.sh")
	if !strings.Contains(runner, scenario+") apply_trashcan ;;") {
		t.Fatal("trashcan scenario has no required apply handler")
	}
	_, handler, ok := strings.Cut(runner, "apply_trashcan() {")
	if !ok {
		t.Fatal("trashcan apply handler is missing")
	}
	handler, _, _ = strings.Cut(handler, "\n}")
	for _, step := range []string{
		"for revision in 1 2", "tf apply -auto-approve", "tf plan -detailed-exitcode",
		`"$here/check-trashcan.py" --check`, "tf import artifactory_trashcan_config.this",
	} {
		if !strings.Contains(handler, step) {
			t.Errorf("trashcan handler lost %s", step)
		}
	}
	if !strings.Contains(runner, `"$here/check-trashcan.py" --deleted`) {
		t.Fatal("trashcan scenario lost its destroy reset check")
	}
	for _, file := range []string{scenario + "/main.tf", scenario + "/versions.tf", "test/terraform/check-trashcan.py"} {
		if provider, _ := terraformPathLanes(file); !provider {
			t.Errorf("%s must select required provider conformance", file)
		}
	}
}
