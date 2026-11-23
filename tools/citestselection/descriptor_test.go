package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestUnitDescriptorProviderControlsAreRequired(t *testing.T) {
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
	const scenario = "deploy/artifactweave/terraform/configuration"
	if !strings.Contains(read("Makefile"), "\t"+scenario) {
		t.Fatal("descriptor controls left the required provider scenarios")
	}
	fixture := read(scenario + "/main.tf")
	if strings.Count("\n"+fixture, "\nresource ") != 2 {
		t.Fatal("descriptor control scope must remain exactly two resources")
	}
	for _, kind := range []string{"artifactory_proxy", "artifactory_property_set"} {
		if strings.Count(fixture, `resource "`+kind+`"`) != 1 {
			t.Fatalf("descriptor controls must manage one %s", kind)
		}
	}
	pin := regexp.MustCompile(`version\s*=\s*"([0-9.]+)"`).FindStringSubmatch(read(scenario + "/versions.tf"))
	if len(pin) != 2 || !strings.Contains(read(scenario+"/.terraform.lock.hcl"), `version     = "`+pin[1]+`"`) {
		t.Fatal("descriptor controls lost the pinned provider lock")
	}
	runner := read("test/terraform/run.sh")
	if !strings.Contains(runner, "ARTIFACTORY_VERSION="+pin[1]+"\n") {
		t.Fatal("descriptor provider pin disagrees with the release mirror")
	}
	if !strings.Contains(runner, scenario+") apply_configuration ;;") {
		t.Fatal("descriptor controls have no required native apply handler")
	}
	_, handler, ok := strings.Cut(runner, "apply_configuration() {")
	if !ok {
		t.Fatal("descriptor provider handler is missing")
	}
	handler, _, _ = strings.Cut(handler, "\n}")
	for _, step := range []string{"for revision in 1 2", "tf apply -auto-approve", "tf apply -refresh-only -auto-approve", "tf plan -detailed-exitcode", "unset TF_VAR_proxy_password", "configuration_proof created", "configuration_proof updated", "configuration_proof refreshed", "configuration_proof retained", "configuration_proof cleared"} {
		if !strings.Contains(handler, step) {
			t.Errorf("descriptor provider handler lost %s", step)
		}
	}
	if !strings.Contains(runner, "tf destroy -auto-approve") || !strings.Contains(runner, `test ! -s "$work/descriptor-remaining-resources.txt"`) {
		t.Fatal("descriptor controls lost their destruction proof")
	}
	if !strings.Contains(runner, "configuration_proof deleted") {
		t.Fatal("configuration scenario lost the actual secret destruction proof")
	}
	if strings.Contains(read(scenario+"/variables.tf"), `variable "revision"`) || strings.Contains(read(scenario+"/variables.tf"), "fixture") {
		t.Fatal("consumer reference includes harness-only inputs")
	}
	for _, file := range []string{scenario + "/main.tf", scenario + "/versions.tf", scenario + "/.terraform.lock.hcl"} {
		if provider, _ := terraformPathLanes(file); !provider {
			t.Errorf("%s must select required provider conformance", file)
		}
	}
}
