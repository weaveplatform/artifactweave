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
	if _, err := scanKinds(root, plan{profile: "format", candidate: affectedPlan{
		units: []string{"./pkg"},
	}}); err == nil {
		t.Fatal("a selected Go unit lane with zero TestUnit entry points passed")
	}
	if pattern := (kindSelection{}).runPattern("format", "acceptance"); pattern != `^\z` {
		t.Fatalf("empty acceptance selection must not run arbitrary tests: %q", pattern)
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
	if rootEntries == 0 || !slices.Contains(selection.Acc, "./test/server/maven") {
		t.Fatalf("Maven acceptance dependencies omitted: %+v", selection)
	}
}
