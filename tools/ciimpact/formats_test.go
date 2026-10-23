package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestAffectedPlanChoosesOnlyOwnedChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes []change
		profile string
		owner   string
	}{
		{"npm implementation", []change{{status: "M", path: "internal/format/npm/handler.go"}}, "format", "npm"},
		{"npm implementation and docs", []change{{status: "M", path: "internal/format/npm/handler.go"}, {status: "M", path: "docs/testing/ci.md"}}, "format", "npm"},
		{"new npm implementation", []change{{status: "A", path: "internal/format/npm/new.go"}}, "format", "npm"},
		{"Terraform fixture", []change{{status: "M", path: "internal/format/terraform/terraformtest/fixture.go"}}, "format", "terraform"},
		{"Helm OCI test", []change{{status: "M", path: "test/server/helmoci/push_test.go"}}, "format", "helmoci"},
		{"deployment only", []change{{status: "M", path: "deploy/check.sh"}}, "deployment", ""},
		{"docs only", []change{{status: "M", path: "README.md"}}, "docs", ""},
		{"shared auth", []change{{status: "M", path: "internal/auth/auth.go"}}, "full", ""},
		{"shared server", []change{{status: "M", path: "internal/server/server.go"}}, "full", ""},
		{"shared fixture", []change{{status: "M", path: "test/server/support/support.go"}}, "full", ""},
		{"new server test", []change{{status: "A", path: "test/server/npm/new_test.go"}}, "full", ""},
		{"unknown root test", []change{{status: "M", path: "internal/server/unknown_test.go"}}, "full", ""},
		{"unknown format", []change{{status: "M", path: "internal/format/newformat/handler.go"}}, "full", ""},
		{"shared format code", []change{{status: "M", path: "internal/format/formats/formats.go"}}, "full", ""},
		{"format and deployment", []change{{status: "M", path: "internal/format/npm/handler.go"}, {status: "M", path: "deploy/check.sh"}}, "full", ""},
		{"two formats", []change{{status: "M", path: "internal/format/npm/handler.go"}, {status: "M", path: "internal/format/maven/handler.go"}}, "full", ""},
		{"deleted source", []change{{status: "D", path: "internal/format/npm/handler.go"}}, "full", ""},
		{"renamed source", []change{{status: "R100", old: "internal/format/npm/old.go", path: "internal/format/npm/new.go"}}, "full", ""},
		{"acceptance feature", []change{{status: "M", path: "test/acceptance/features/npm.feature"}}, "full", ""},
		{"dependencies", []change{{status: "M", path: "go.mod"}}, "full", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := proposeAffected(tc.changes)
			if p.profile != tc.profile || p.owner != tc.owner {
				t.Fatalf("plan = %+v, want profile %s, owner %s", p, tc.profile, tc.owner)
			}
		})
	}
	npm := proposeAffected([]change{{status: "M", path: "internal/format/npm/handler.go"}})
	for _, pkg := range []string{"./internal/format/npm/...", "./internal/server/npm", "./test/server/npm", "./internal/server"} {
		if !slices.Contains(npm.units, pkg) {
			t.Errorf("npm plan omitted %s", pkg)
		}
	}
	docker := proposeAffected([]change{{status: "M", path: "internal/format/docker/handler.go"}})
	for _, pkg := range []string{"./test/server/docker", "./test/server/helmoci", "./internal/server/nimmodel"} {
		if !slices.Contains(docker.units, pkg) {
			t.Errorf("Docker plan omitted dependent package %s", pkg)
		}
	}
}

func TestFormatImpactCoversOwnedServerPackagesAndKnownTags(t *testing.T) {
	inventory, err := loadTestOwnership()
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range inventory.Packages {
		if owner != "shared" {
			if _, ok := formats[owner]; !ok {
				t.Errorf("server test owner %s has no affected plan", owner)
			}
		}
	}
	tags := map[string]bool{}
	features, err := filepath.Glob("../../test/acceptance/features/*.feature")
	if err != nil {
		t.Fatal(err)
	}
	matcher := regexp.MustCompile(`@[A-Za-z][A-Za-z0-9_-]*`)
	for _, feature := range features {
		body, err := os.ReadFile(feature)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range matcher.FindAllString(string(body), -1) {
			tags[tag] = true
		}
	}
	for owner, conf := range formats {
		if conf.tags == "" {
			t.Errorf("%s has no native acceptance selection", owner)
		}
		for _, tag := range matcher.FindAllString(conf.tags, -1) {
			if !tags[tag] {
				t.Errorf("%s selects unknown acceptance tag %s", owner, tag)
			}
		}
		if strings.Contains(conf.tags, "@nuget") && owner != "nuget" {
			t.Errorf("%s crosses the separate NuGet acceptance partition", owner)
		}
	}
}
