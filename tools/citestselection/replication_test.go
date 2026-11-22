package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUnitSingleReplicationProviderScenarioIsRequired(t *testing.T) {
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
		return string(data)
	}
	const scenario = "deploy/artifactweave/terraform/single-replication"
	if !strings.Contains(read("Makefile"), "\t"+scenario) {
		t.Fatal("single-replication fixture left the required provider scenarios")
	}
	for _, resource := range []string{"artifactory_local_repository_single_replication", "artifactory_remote_repository_replication"} {
		if strings.Count(read(scenario+"/main.tf"), `resource "`+resource+`"`) != 1 {
			t.Errorf("native fixture must contain exactly one %s", resource)
		}
	}
	if !strings.Contains(read("test/terraform/run.sh"), scenario+") apply_single_replication ;;") {
		t.Fatal("required provider runner lost the single-replication apply steps")
	}
	for _, file := range []string{scenario + "/main.tf", "test/terraform/check-replications.py"} {
		if selected, _ := terraformPathLanes(file); !selected {
			t.Errorf("%s does not select native provider conformance", file)
		}
	}
}

func TestUnitReplicationWorkspaceContractsBelongToProviderLane(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	const source = "test/acceptance/replication_provider_workspace_test.go"
	candidate := proposeAffected([]change{{status: "M", path: source}})
	provider, _ := (plan{profile: candidate.profile, candidate: candidate}).terraformLanes()
	if candidate.profile != "full" || !provider {
		t.Fatal("caller workspace changes must select full qualification and the provider lane")
	}
	data, err := os.ReadFile(filepath.Join(root, ".github/workflows/quality-gate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var selected *regexp.Regexp
	for _, step := range workflow.Jobs["terraform"].Steps {
		if step.Env["ARTIFACTWEAVE_ACCEPT_TAGS"] != "@replication_provider" {
			continue
		}
		match := runArgument.FindStringSubmatch(step.Run)
		if match == nil || selected != nil {
			t.Fatal("provider lane needs one unambiguous Go test selection")
		}
		selected, err = regexp.Compile(match[1] + match[2] + match[3])
		if err != nil {
			t.Fatal(err)
		}
	}
	if selected == nil || !selected.MatchString("TestAccFeatures") || selected.MatchString("TestAccFeaturesUnrelated") {
		t.Fatal("required provider scenarios lost their exact selected entry point")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, source), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	contracts := 0
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || !strings.HasPrefix(function.Name.Name, "TestUnitReplicationProvider") {
			continue
		}
		contracts++
		if !selected.MatchString(function.Name.Name) {
			t.Errorf("provider lane omits real workspace contract %s", function.Name.Name)
		}
	}
	if contracts < 2 {
		t.Fatal("workspace continuation and invalid-output contracts are missing")
	}
}
