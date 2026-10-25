package acceptancepartition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"
	"gopkg.in/yaml.v3"
)

// The workflow filters must partition actual compiled scenarios, including
// Scenario Outline examples, without losing untagged scenarios.
func TestUnitWorkflowAcceptancePartitions(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Jobs map[string]struct {
			Needs []string `yaml:"needs"`
			If    string   `yaml:"if"`
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &parsed); err != nil {
		t.Fatal(err)
	}
	filters := map[string]struct{ step, expression string }{
		"acceptance": {
			"Acceptance features (coverage-instrumented binary)",
			"${{ needs.impact.outputs.profile == 'full' && '~@nuget&&~@gradle&&~@bowerlive' || needs.impact.outputs.tags }}",
		},
		"acceptance-nuget": {
			"NuGet acceptance features (coverage-instrumented binary)",
			"@nuget&&~@gradle",
		},
		"acceptance-gradle": {"Gradle features (informational)", "@gradle"},
	}
	for job, want := range filters {
		found := false
		for _, step := range parsed.Jobs[job].Steps {
			if step.Name == want.step {
				if got := step.Env["ARTIFACTWEAVE_ACCEPT_TAGS"]; got != "" {
					if got != want.expression {
						t.Errorf("%s: filter = %q, want %q", job, got, want.expression)
					}
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: missing tagged acceptance step", job)
		}
	}
	contains := func(items []string, want string) bool {
		for _, item := range items {
			if item == want {
				return true
			}
		}
		return false
	}
	for _, required := range []string{"acceptance", "acceptance-nuget"} {
		for _, dependent := range []string{"coverage", "gate"} {
			if !contains(parsed.Jobs[dependent].Needs, required) {
				t.Errorf("%s does not require %s", dependent, required)
			}
		}
	}
	if !contains(parsed.Jobs["coverage"].Needs, "acceptance-gradle") ||
		!strings.HasPrefix(parsed.Jobs["coverage"].If, "always()") ||
		!strings.Contains(parsed.Jobs["coverage"].If, "needs.impact.outputs.profile == 'full'") {
		t.Error("full coverage must await optional Gradle and run even if it fails")
	}
	if contains(parsed.Jobs["gate"].Needs, "acceptance-gradle") {
		t.Error("informational Gradle must not be a Quality gate dependency")
	}
	for _, job := range []string{"acceptance", "acceptance-nuget"} {
		found := false
		for _, step := range parsed.Jobs[job].Steps {
			if strings.HasPrefix(step.Name, "Upload ") &&
				strings.Contains(step.Name, "acceptance coverage") {
				found = true
				if step.With["if-no-files-found"] != "error" {
					t.Errorf("%s must fail when its coverage artifact is empty", job)
				}
			}
		}
		if !found {
			t.Errorf("%s has no mandatory coverage upload", job)
		}
	}

	paths, err := filepath.Glob("../../test/acceptance/features/*.feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no acceptance features")
	}
	id := (&messages.Incrementing{}).NewId
	counts := map[string]int{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, parseErr := gherkin.ParseGherkinDocument(file, id)
		closeErr := file.Close()
		if parseErr != nil {
			t.Fatalf("%s: %v", path, parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		for _, pickle := range gherkin.Pickles(*doc, path, id) {
			tags := map[string]bool{}
			for _, tag := range pickle.Tags {
				tags[tag.Name] = true
			}
			if strings.HasPrefix(filepath.Base(path), "nuget_") && !tags["@nuget"] {
				t.Errorf("%s: NuGet scenario %q lacks @nuget", path, pickle.Name)
			}
			selected := "acceptance"
			if tags["@bowerlive"] {
				selected = "external"
			} else if tags["@gradle"] {
				selected = "acceptance-gradle"
			} else if tags["@nuget"] {
				selected = "acceptance-nuget"
			}
			counts[selected]++
			if selected == "acceptance" {
				for _, step := range pickle.Steps {
					lower := strings.ToLower(step.Text)
					if strings.Contains(lower, "dotnet") || strings.Contains(lower, ".net") {
						t.Errorf(
							"%s: core scenario %q uses an unprepared .NET client",
							path,
							pickle.Name,
						)
					}
				}
			}
		}
	}
	for job := range filters {
		if counts[job] == 0 {
			t.Errorf("%s selects no compiled scenarios", job)
		}
	}
	t.Logf(
		"compiled scenarios by partition: core=%d NuGet=%d Gradle=%d",
		counts["acceptance"],
		counts["acceptance-nuget"],
		counts["acceptance-gradle"],
	)
}
