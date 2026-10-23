package main

import (
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
		{"unknown format", []change{{status: "M", path: "internal/format/newformat/handler.go"}}, "full", ""},
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
