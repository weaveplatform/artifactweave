package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The general security control is required provider conformance: each
// fixture below removes one piece of it, and the checker's inventory must
// refuse every one rather than let the lane pass without the proof.
func TestUnitGeneralSecurityProviderScenarioIsRequired(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	const scenario = "deploy/artifactweave/terraform/general-security"
	checker := filepath.Join(root, "test/terraform/check-general-security.py")
	if out, err := exec.Command("python3", "-I", checker, "--inventory", root).CombinedOutput(); err != nil {
		t.Fatalf("required general security inventory: %v: %s", err, out)
	}
	for _, path := range []string{
		"test/terraform/check-general-security.py",
		scenario + "/main.tf",
		scenario + "/.terraform.lock.hcl",
	} {
		provider, _ := terraformPathLanes(path)
		if !provider {
			t.Errorf("%s must select native provider conformance", path)
		}
	}
	files := []string{
		"Makefile", "test/terraform/run.sh",
		scenario + "/main.tf", scenario + "/versions.tf", scenario + "/.terraform.lock.hcl",
	}
	for _, tc := range []struct {
		name, file, before, after string
	}{
		{"omitted native scenario", "Makefile", scenario, "omitted-general-security-scenario"},
		{"omitted apply handler", "test/terraform/run.sh", scenario + ") apply_general_security ;;", "omitted-general-security-handler"},
		{"omitted effect checks", "test/terraform/run.sh", `python3 -I "$here/check-general-security.py" --check`, "omitted-general-security-effect-checks"},
		{"omitted default checks", "test/terraform/run.sh", `python3 -I "$here/check-general-security.py" --deleted`, "omitted-general-security-default-checks"},
		{"omitted resource", scenario + "/main.tf", `resource "artifactory_general_security"`, `resource "omitted_general_security"`},
		{"wrong provider lock", scenario + "/.terraform.lock.hcl", `provider "registry.terraform.io/jfrog/artifactory"`, `provider "registry.terraform.io/jfrog/omitted-artifactory"`},
		{"unpinned release", "test/terraform/run.sh", "\nARTIFACTORY_VERSION=", "\nOMITTED_ARTIFACTORY_VERSION="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := t.TempDir()
			for _, file := range files {
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
				if err != nil {
					t.Fatal(err)
				}
				if file == tc.file {
					// Windows checkouts may convert to CRLF; the anchors match lines.
					before := strings.ReplaceAll(string(data), "\r\n", "\n")
					data = []byte(strings.ReplaceAll(before, tc.before, tc.after))
					if string(data) == before {
						t.Fatal("inventory refusal fixture no longer changes its input")
					}
				}
				path := filepath.Join(fixture, filepath.FromSlash(file))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := exec.Command("python3", "-I", checker, "--inventory", fixture).CombinedOutput(); err == nil {
				t.Fatalf("missing required provider proof became a pass: %s", out)
			}
		})
	}
}
