package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitCleanupProviderScenarioIsRequired(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(root, "test/terraform/check-cleanup.py")
	if out, err := exec.Command("python3", "-I", checker, "--inventory", root).CombinedOutput(); err != nil {
		t.Fatalf("required cleanup inventory: %v: %s", err, out)
	}
	const scenario = "deploy/artifactweave/terraform/package-cleanup"
	for _, path := range []string{"test/terraform/check-cleanup.py", scenario + "/main.tf", scenario + "/.terraform.lock.hcl"} {
		provider, _ := terraformPathLanes(path)
		if !provider {
			t.Errorf("%s must select native provider conformance", path)
		}
	}
	files := []string{"Makefile", "test/terraform/run.sh", scenario + "/main.tf", scenario + "/versions.tf", scenario + "/.terraform.lock.hcl"}
	for _, tc := range []struct{ name, file, before, after string }{
		{"omitted scenario", "Makefile", scenario, "omitted-cleanup"},
		{"omitted apply handler", "test/terraform/run.sh", scenario + ") apply_package_cleanup ;;", "omitted-cleanup-handler"},
		{"omitted dry proof", "test/terraform/run.sh", `"$here/check-cleanup.py" --prepare`, "omitted-dry-proof"},
		{"omitted actual proof", "test/terraform/run.sh", `"$here/check-cleanup.py" --check`, "omitted-actual-proof"},
		{"omitted destroy proof", "test/terraform/run.sh", `"$here/check-cleanup.py" --deleted`, "omitted-destroy-proof"},
		{"omitted resource", scenario + "/main.tf", `resource "artifactory_package_cleanup_policy"`, `resource "omitted_cleanup"`},
		{"wrong provider", scenario + "/.terraform.lock.hcl", `provider "registry.terraform.io/jfrog/artifactory"`, `provider "registry.terraform.io/jfrog/omitted"`},
		{"old Terraform", scenario + "/versions.tf", ">= 1.17", ">= 1.16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := t.TempDir()
			for _, file := range files {
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
				if err != nil {
					t.Fatal(err)
				}
				if file == tc.file {
					before := string(data)
					data = []byte(strings.ReplaceAll(before, tc.before, tc.after))
					if string(data) == before {
						t.Fatal("inventory refusal fixture did not change its input")
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
				t.Fatalf("missing required cleanup proof became a pass: %s", out)
			}
		})
	}
}
