package main

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"
)

func TestUnitAffectedPlanChoosesOnlyOwnedChanges(t *testing.T) {
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
		{"agent packages server adapter", []change{{status: "M", path: "internal/server/agentpackages/handler.go"}}, "format", "agentpackages"},
		{"Helm OCI test", []change{{status: "M", path: "test/server/helmoci/push_test.go"}}, "format", "helmoci"},
		{"deployment only", []change{{status: "M", path: "deploy/check.sh"}}, "deployment", ""},
		{"docs only", []change{{status: "M", path: "README.md"}}, "docs", ""},
		{"deployment guide only", []change{{status: "M", path: "deploy/platform/terraform/aws/cdn/README.md"}}, "docs", ""},
		{"deployment guide and code", []change{{status: "M", path: "deploy/platform/terraform/aws/cdn/README.md"}, {status: "M", path: "deploy/check.sh"}}, "deployment", ""},
		{"deployment template", []change{{status: "M", path: "deploy/platform/SCENARIO-TEMPLATE.md"}}, "deployment", ""},
		{"fixture readme", []change{{status: "M", path: "test/acceptance/testdata/upstreams/README.md"}}, "full", ""},
		{"shared auth", []change{{status: "M", path: "internal/auth/auth.go"}}, "full", ""},
		{"shared server", []change{{status: "M", path: "internal/server/server.go"}}, "full", ""},
		{"shared fixture", []change{{status: "M", path: "test/server/support/support.go"}}, "full", ""},
		{"new server test", []change{{status: "A", path: "test/server/npm/new_test.go"}}, "full", ""},
		{"unknown root test", []change{{status: "M", path: "internal/server/unknown_test.go"}}, "full", ""},
		{"unknown format", []change{{status: "M", path: "migration/artifacts/ecosystem/newformat/handler.go"}}, "full", ""},
		{"shared format code", []change{{status: "M", path: "internal/format/formats/formats.go"}}, "full", ""},
		{"format and deployment", []change{{status: "M", path: "internal/format/npm/handler.go"}, {status: "M", path: "deploy/check.sh"}}, "full", ""},
		{"two formats", []change{{status: "M", path: "internal/format/npm/handler.go"}, {status: "M", path: "internal/format/maven/handler.go"}}, "full", ""},
		{"deleted source", []change{{status: "D", path: "internal/format/npm/handler.go"}}, "full", ""},
		{"renamed source", []change{{status: "R100", old: "internal/format/npm/old.go", path: "internal/format/npm/new.go"}}, "full", ""},
		{"acceptance feature", []change{{status: "M", path: "test/acceptance/features/npm.feature"}}, "full", ""},
		{"format fixture", []change{{status: "M", path: "internal/format/npm/testdata/metadata.json"}}, "full", ""},
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
	conan := proposeAffected([]change{{status: "M", path: "internal/server/conan/remote.go"}})
	if conan.profile != "format" || conan.owner != "conan" {
		t.Fatalf("Conan source selection = %+v", conan)
	}
	for _, pkg := range []string{"./internal/format/conan/...", "./internal/server/conan", "./internal/server"} {
		if !slices.Contains(conan.units, pkg) {
			t.Errorf("Conan plan omitted %s", pkg)
		}
	}
	for _, pkg := range []string{"internal/format/conan/...", "internal/server/conan/..."} {
		if !strings.Contains(conan.coverpkg, pkg) {
			t.Errorf("Conan plan did not instrument %s", pkg)
		}
	}
	docker := proposeAffected([]change{{status: "M", path: "internal/format/docker/handler.go"}})
	for _, pkg := range []string{"./test/server/docker", "./test/server/helmoci", "./internal/server/nimmodel"} {
		if !slices.Contains(docker.units, pkg) {
			t.Errorf("Docker plan omitted dependent package %s", pkg)
		}
	}
	agent := proposeAffected(
		[]change{{status: "M", path: "internal/server/agentpackages/handler.go"}},
	)
	if !strings.Contains(
		agent.coverpkg,
		"github.com/weaveplatform/artifactweave/internal/server/agentpackages",
	) {
		t.Fatalf("changed production package is not instrumented: %+v", agent)
	}
}

func TestUnitFormatImpactCoversOwnedServerPackagesAndKnownTags(t *testing.T) {
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
	var pickles []*messages.Pickle
	var id int
	nextID := func() string { id++; return strconv.Itoa(id) }
	for _, feature := range features {
		body, err := os.ReadFile(feature)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range matcher.FindAllString(string(body), -1) {
			tags[tag] = true
		}
		document, err := gherkin.ParseGherkinDocumentForLanguage(
			strings.NewReader(string(body)),
			"en",
			nextID,
		)
		if err != nil {
			t.Fatalf("parse %s: %v", feature, err)
		}
		pickles = append(pickles, gherkin.Pickles(*document, feature, nextID)...)
	}
	for owner, conf := range formats {
		if conf.tags == "" {
			t.Errorf("%s has no native acceptance selection", owner)
		}
		if strings.ContainsAny(conf.tags, "|()") {
			t.Errorf("%s uses unsupported Godog tag syntax %q", owner, conf.tags)
		}
		for _, tag := range matcher.FindAllString(conf.tags, -1) {
			if !tags[tag] {
				t.Errorf("%s selects unknown acceptance tag %s", owner, tag)
			}
		}
		if strings.Contains(conf.tags, "@nuget") && owner != "nuget" {
			t.Errorf("%s crosses the separate NuGet acceptance partition", owner)
		}
		selected := 0
		for _, pickle := range pickles {
			if matchesGodogTags(conf.tags, pickle.Tags) {
				selected++
			}
		}
		if selected == 0 {
			t.Errorf("%s tag filter %q selects no acceptance scenarios", owner, conf.tags)
		}
	}
}

// Godog v0.16 uses comma for OR and && for AND. Keep the map's expressions
// checked against the actual feature pickles so a typo cannot silently run 0.
func matchesGodogTags(filter string, tags []*messages.PickleTag) bool {
	for _, alternative := range strings.Split(filter, ",") {
		matches := true
		for _, part := range strings.Split(alternative, "&&") {
			part = strings.TrimSpace(part)
			negated := strings.HasPrefix(part, "~")
			name := strings.TrimPrefix(part, "~")
			found := false
			for _, tag := range tags {
				if tag.Name == name {
					found = true
					break
				}
			}
			if found == negated {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func TestUnitFormatSourceOwnerStagedLayouts(t *testing.T) {
	useTestOwnership(t, testOwnership{
		Version: 2,
		Packages: map[string]string{
			"internal/artifacts/ecosystem/npm/http":  "npm",
			"migration/app":                          "shared",
			"migration/artifacts/adapters/http":      "shared",
			"migration/artifacts/ecosystem/npm/http": "npm",
			"test/server/artifacts":                  "shared",
			"test/server/artifacts/helm":             "shared",
			"test/server/artifacts/npm":              "npm",
			"test/server/npm":                        "npm",
		},
		SharedFiles: map[string][]string{
			"migration/app":         {"router_test.go"},
			"test/server/artifacts": {"items_test.go"},
		},
		FormatDependencies: map[string][]string{"npm": {"migration/app/router_test.go"}},
	})
	for file, want := range map[string]string{
		"migration/artifacts/ecosystem/npm/tarball.go":           "npm",
		"migration/artifacts/ecosystem/npm/tarball_test.go":      "npm",
		"internal/artifacts/ecosystem/npm/tarball.go":            "npm",
		"migration/artifacts/ecosystem/npm/http/routes.go":       "npm",
		"migration/artifacts/ecosystem/npm/http/routes_test.go":  "npm",
		"internal/artifacts/ecosystem/npm/http/routes_test.go":   "npm",
		"test/server/artifacts/npm/publish_test.go":              "npm",
		"test/server/artifacts/npm/fixture.go":                   "npm",
		"internal/format/npm/handler.go":                         "npm",
		"test/server/npm/publish_test.go":                        "npm",
		"migration/artifacts/ecosystem/pypi/http/routes_test.go": "",
		"internal/artifacts/ecosystem/pypi/http/routes_test.go":  "",
		"test/server/artifacts/helm/push_test.go":                "",
		"test/server/artifacts/items_test.go":                    "",
		"test/server/artifacts/fixture.go":                       "",
		"migration/artifacts/ecosystem/newformat/handler.go":     "",
		"migration/artifacts/ecosystem/npm.go":                   "",
		"migration/artifacts/core/item/item.go":                  "",
		"migration/artifacts/adapters/http/items.go":             "",
		"migration/app/router_test.go":                           "",
		"migration/app/app.go":                                   "",
		"pkg/artifacts/ecosystem/npm/handler.go":                 "",
		"migration/artifacts/ecosystem/npm/http/README.md":       "",
	} {
		owner, ok := formatSourceOwner(file)
		if owner != want || ok != (want != "") {
			t.Errorf("formatSourceOwner(%q) = %q, %t; want %q", file, owner, ok, want)
		}
	}
	staged := proposeAffected([]change{
		{status: "M", path: "migration/artifacts/ecosystem/npm/tarball.go"},
		{status: "A", path: "migration/artifacts/ecosystem/npm/http/routes.go"},
		{status: "M", path: "test/server/artifacts/npm/fixture.go"},
	})
	want := []string{
		modulePath + "/migration/artifacts/ecosystem/npm",
		modulePath + "/migration/artifacts/ecosystem/npm/http",
	}
	if staged.profile != "format" || staged.owner != "npm" ||
		!slices.Equal(staged.changedPackages, want) {
		t.Fatalf("staged npm plan: %+v", staged)
	}
	for _, pkg := range append(want, modulePath+"/migration/artifacts/ecosystem/npm/http/...") {
		if !slices.Contains(strings.Split(staged.coverpkg, ","), pkg) {
			t.Errorf("staged npm plan does not instrument %s: %s", pkg, staged.coverpkg)
		}
	}
	if !slices.Contains(staged.units, "./migration/app") || !staged.hasSharedTests {
		t.Errorf("staged npm plan omitted its shared app dependency: %+v", staged)
	}
}

// legacyCoverPackages is the cover list version 1 built from the owner's
// name, kept only to prove the derived list drops nothing that exists.
func legacyCoverPackages(owner string, conf formatImpact, inventory testOwnership) []string {
	var coverPackages []string
	if conf.unitRoot != "" {
		coverPackages = append(coverPackages, modulePath+"/internal/format/"+owner+"/...")
	}
	if _, ok := inventory.Packages["internal/server/"+owner]; ok {
		coverPackages = append(coverPackages, modulePath+"/internal/server/"+owner+"/...")
	}
	if len(coverPackages) == 0 {
		coverPackages = append(coverPackages, modulePath+"/internal/server/docker/...")
	}
	return coverPackages
}

func TestUnitCoverPackagesUnchangedForCurrentInventory(t *testing.T) {
	inventory, err := loadTestOwnership()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean("../..")
	exists := func(pattern string) bool {
		dir := strings.TrimSuffix(strings.TrimPrefix(pattern, modulePath+"/"), "/...")
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir)))
		return err == nil && info.IsDir()
	}
	dropped, added := map[string]bool{}, map[string]bool{}
	for owner, conf := range formats {
		derived := ownerCoverPackages(owner, conf, inventory)
		legacy := legacyCoverPackages(owner, conf, inventory)
		for _, pkg := range legacy {
			if slices.Contains(derived, pkg) {
				continue
			}
			if exists(pkg) {
				t.Errorf("%s: derived cover list drops existing %s", owner, pkg)
			}
			dropped[owner+" "+pkg] = true
		}
		for _, pkg := range derived {
			if !exists(pkg) {
				t.Errorf("%s: derived cover list names missing %s", owner, pkg)
			}
			if !slices.Contains(legacy, pkg) {
				added[owner+" "+pkg] = true
			}
		}
	}
	t.Logf("dropped (directory missing): %q", slices.Sorted(maps.Keys(dropped)))
	t.Logf("added (owner's existing packages): %q", slices.Sorted(maps.Keys(added)))
}
