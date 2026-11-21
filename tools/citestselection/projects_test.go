package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitProjectProviderScenarioIsRequired(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(root, "test/terraform/check-projects.py")
	if out, err := exec.Command("python3", "-I", checker, "--inventory", root).CombinedOutput(); err != nil {
		t.Fatalf("required project inventory: %v: %s", err, out)
	}
	for _, path := range []string{
		"test/terraform/check-projects.py",
		"deploy/artifactweave/terraform/projects/main.tf",
		"deploy/artifactweave/terraform/projects/.terraform.lock.hcl",
	} {
		provider, _ := terraformPathLanes(path)
		if !provider {
			t.Errorf("%s must select native provider conformance", path)
		}
	}
	files := []string{
		"Makefile", "test/terraform/run.sh",
		"deploy/artifactweave/terraform/projects/main.tf",
		"deploy/artifactweave/terraform/projects/versions.tf",
		"deploy/artifactweave/terraform/projects/.terraform.lock.hcl",
	}
	for _, tc := range []struct {
		name, file, before, after string
	}{
		{"omitted native scenario", "Makefile", "deploy/artifactweave/terraform/projects", "omitted-project-scenario"},
		{"omitted apply handler", "test/terraform/run.sh", "deploy/artifactweave/terraform/projects) apply_projects ;;", "omitted-project-handler"},
		{"omitted effect checks", "test/terraform/run.sh", `python3 -I "$here/check-projects.py" --check`, "omitted-project-effect-checks"},
		{"omitted destroy checks", "test/terraform/run.sh", `python3 -I "$here/check-projects.py" --deleted`, "omitted-project-destroy-checks"},
		{"omitted planned resource", "deploy/artifactweave/terraform/projects/main.tf", `resource "project_environment"`, `resource "omitted_project_environment"`},
		{"wrong provider lock", "deploy/artifactweave/terraform/projects/.terraform.lock.hcl", `provider "registry.terraform.io/jfrog/project"`, `provider "registry.terraform.io/jfrog/omitted-project"`},
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
