package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
