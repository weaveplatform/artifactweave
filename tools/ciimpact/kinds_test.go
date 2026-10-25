package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestUnitSelectKindsKeepsMixedPackagesAndBuildTaggedTests(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"internal/server/repositories_test.go": `package server
func TestUnitRead() {}
func TestAccPublish() {}
func TestAccess() {}
`,
		"internal/server/unrelated_test.go": `package server
func TestAccUnrelated() {}
`,
		"internal/server/windows_test.go": `//go:build windows
package server
func TestUnitWindows() {}
`,
		"internal/format/demo/format_test.go": `package demo
func TestUnitFormat() {}
`,
		"pkg/seed_test.go": `package pkg
func FuzzSeed() {}
`,
		"test/acceptance/features_test.go": `package acceptance
func TestAccFeatures() {}
`,
		"test/platform/cloud_test.go": `package platform
func TestAccCloud() {}
`,
	} {
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, area := range []string{"api", "tools"} {
		if err := os.Mkdir(filepath.Join(root, area), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	full, err := scanKinds(root, plan{profile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(full.Unit, []string{"./internal/format/demo", "./internal/server"}) ||
		!slices.Equal(full.Acc, []string{"./internal/server"}) ||
		!slices.Equal(full.Fuzz, []string{"./pkg"}) {
		t.Fatalf("full test kinds: %+v", full)
	}
	buildTagged := false
	for _, entry := range full.Selected {
		if entry.Name == "TestAccess" || strings.HasPrefix(entry.Package, "./test/") {
			t.Errorf("selected unclassified or independently owned test: %+v", entry)
		}
		if entry.Name == "TestUnitWindows" &&
			entry.Source == "internal/server/windows_test.go" {
			buildTagged = true
		}
	}
	if !buildTagged {
		t.Fatal("Windows-only TestUnit source was lost on this host")
	}
	format, err := scanKinds(root, plan{profile: "format", candidate: affectedPlan{
		owner: "nuget", units: []string{"./internal/server", "./internal/format/demo/..."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(format.Unit, full.Unit) || !slices.Equal(format.Acc, full.Acc) ||
		len(format.Fuzz) != 0 {
		t.Fatalf("format test kinds: %+v", format)
	}
	if strings.Contains(format.runPattern("format", "acceptance"), "TestAccUnrelated") ||
		!strings.Contains(format.runPattern("format", "acceptance"), "TestAccPublish") {
		t.Fatalf("root test ownership leaked an unrelated acceptance suite: %+v", format)
	}
	for _, tc := range []struct {
		kind, selected, extra, unrelated string
	}{
		{"unit", "TestUnitRead", "TestUnitReadExtra", "TestAccPublish"},
		{"acceptance", "TestAccPublish", "TestAccPublishExtra", "TestAccUnrelated"},
	} {
		pattern, err := regexp.Compile(format.runPattern("format", tc.kind))
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(tc.selected) || pattern.MatchString(tc.extra) ||
			pattern.MatchString(tc.unrelated) {
			t.Errorf("%s selection is not exact: %q", tc.kind, pattern)
		}
	}
	fuzzOnly, err := scanKinds(root, plan{profile: "format", candidate: affectedPlan{
		units: []string{"./pkg"},
	}})
	if err != nil || len(fuzzOnly.Unit) != 0 || !slices.Equal(fuzzOnly.Fuzz, []string{"./pkg"}) ||
		!fuzzOnly.hasUnit() {
		t.Fatalf("fuzz-only format selection: %+v, %v", fuzzOnly, err)
	}
	if _, err := scanKinds(root, plan{profile: "format", candidate: affectedPlan{
		units: []string{"./api"},
	}}); err == nil {
		t.Fatal("a format with no Go tests or fuzz seeds was accepted")
	}
	if pattern := (kindSelection{}).runPattern("format", "acceptance"); pattern != `^\z` {
		t.Fatalf("empty acceptance selection must not run arbitrary tests: %q", pattern)
	}
}

func TestUnitAcceptanceOnlyOwnersKeepNarrowSelection(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"test/server/helmoci/helmoci_test.go",
		"internal/server/nimmodel/nimmodel.go",
	} {
		candidate := proposeAffected([]change{{status: "M", path: path}})
		if candidate.profile != "format" {
			t.Fatalf("%s: %+v", path, candidate)
		}
		selected, err := scanKinds(root, plan{profile: "format", candidate: candidate})
		if err != nil || len(selected.Acc) == 0 || selected.hasUnit() {
			t.Fatalf("%s: acceptance-only owner expanded from format: %+v, %v", path, selected, err)
		}
	}
}

func TestUnitEveryMappedFormatOwnerHasAnExplicitGoKind(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for owner := range formats {
		candidate := proposeAffected([]change{{
			status: "M", path: "internal/format/" + owner + "/format.go",
		}})
		if candidate.profile != "format" {
			t.Errorf("%s: %+v", owner, candidate)
			continue
		}
		if _, err := scanKinds(root, plan{profile: "format", candidate: candidate}); err != nil {
			t.Errorf("%s has no owned Go kind: %v", owner, err)
		}
	}
}

func TestUnitTerraformPathsSelectOnlyTheirAffectedLane(t *testing.T) {
	for _, tc := range []struct {
		path                 string
		provider, deployment bool
	}{
		{"deploy/artifactweave/terraform/repositories/main.tf", true, false},
		{"test/terraform/run.sh", true, false},
		{"deploy/platform/terraform/modules/aws/cdn/main.tf", false, true},
		{"deploy/ci/terraform/aws/backend.tf", false, true},
		{"deploy/check.sh", true, true},
	} {
		provider, deployment := terraformPathLanes(tc.path)
		if provider != tc.provider || deployment != tc.deployment {
			t.Errorf("%s: provider=%t deployment=%t", tc.path, provider, deployment)
		}
		candidate := proposeAffected([]change{{status: "M", path: tc.path}})
		if candidate.profile != "deployment" || candidate.provider != tc.provider ||
			candidate.deployment != tc.deployment {
			t.Errorf("%s: %+v", tc.path, candidate)
		}
	}
}

func TestUnitAffectedFormatSelectsOwnedMixedPackageEntries(t *testing.T) {
	candidate := proposeAffected([]change{{status: "M", path: "internal/format/maven/maven.go"}})
	if candidate.profile != "format" || candidate.owner != "maven" {
		t.Fatalf("Maven impact: %+v", candidate)
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	selection, err := scanKinds(root, plan{profile: "format", candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loadTestOwnership()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, file := range inventory.RootFormatDependencies["maven"] {
		allowed[file] = true
	}
	rootEntries := 0
	for _, entry := range selection.Selected {
		if entry.Package != "./internal/server" {
			continue
		}
		rootEntries++
		if !allowed[filepath.Base(entry.Source)] {
			t.Errorf("unrelated root suite selected: %+v", entry)
		}
	}
	if rootEntries == 0 || !slices.Contains(selection.Acc, "./test/server/maven") ||
		!slices.Contains(selection.Acc, "./internal/app") {
		t.Fatalf("Maven acceptance dependencies omitted: %+v", selection)
	}
	appEntries := 0
	for _, entry := range selection.Selected {
		if entry.Package != "./internal/app" {
			continue
		}
		appEntries++
		if entry.Source != "internal/app/maven_remote_test.go" {
			t.Errorf("unrelated app suite selected: %+v", entry)
		}
	}
	if appEntries == 0 {
		t.Fatal("Maven app acceptance tests were omitted")
	}
}

func TestUnitFormatAppDependenciesSelectOnlyOwnedFiles(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for owner, file := range map[string]string{
		"docker": "registry_remote_test.go",
		"maven":  "maven_remote_test.go",
		"nuget":  "nuget_reindex_test.go",
	} {
		t.Run(owner, func(t *testing.T) {
			candidate := proposeAffected([]change{{
				status: "M", path: "internal/format/" + owner + "/handler.go",
			}})
			if candidate.profile != "format" ||
				!slices.Contains(candidate.units, "./internal/app") {
				t.Fatalf("app owner omitted: %+v", candidate)
			}
			selection, err := scanKinds(root, plan{profile: "format", candidate: candidate})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range selection.Selected {
				if entry.Package != "./internal/app" {
					continue
				}
				if entry.Source != "internal/app/"+file || entry.Kind != "acceptance" {
					t.Errorf("unowned app test selected: %+v", entry)
				}
				found = true
			}
			if !found {
				t.Fatalf("owned app test %s omitted", file)
			}
		})
	}
}
