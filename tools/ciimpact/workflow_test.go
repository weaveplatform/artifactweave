package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkflowKeepsOneRequiredGateForSelectedProfiles(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Needs []string `yaml:"needs"`
			If    string   `yaml:"if"`
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if _, ok := workflow.On["push"]; ok {
		t.Fatal("push-to-main would repeat the full gate after every merge")
	}
	if _, ok := workflow.On["pull_request"]; !ok {
		t.Fatal("PR trigger is required")
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("PR and manual full qualification triggers are required")
	}
	fullJobs := []string{"vuln", "build", "acceptance-gradle", "oci-conformance", "parity"}
	for _, name := range fullJobs {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || job.If != "needs.impact.outputs.profile == 'full'" {
			t.Errorf("%s is not gated by the full impact profile", name)
		}
	}
	for name, other := range map[string]string{
		"unit": "format", "lint": "format", "terraform": "deployment", "deploy-terraform": "deployment",
	} {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || !strings.Contains(job.If, "profile == 'full'") ||
			!strings.Contains(job.If, "profile == '"+other+"'") {
			t.Errorf("%s is not gated by full or %s", name, other)
		}
	}
	for _, name := range []string{"acceptance", "acceptance-nuget"} {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || !strings.Contains(job.If, "profile == 'full'") ||
			!strings.Contains(job.If, "profile == 'format'") {
			t.Errorf("%s is not gated by full or format", name)
		}
	}
	if job := workflow.Jobs["docs"]; !contains(job.Needs, "impact") ||
		job.If != "needs.impact.outputs.profile == 'docs'" {
		t.Error("documentation checks are not gated by the docs profile")
	}
	coverage := workflow.Jobs["coverage"]
	if !contains(coverage.Needs, "impact") ||
		!strings.Contains(coverage.If, "needs.impact.outputs.profile == 'full'") {
		t.Error("full coverage does not depend on the impact plan")
	}
	affected := workflow.Jobs["affected-coverage"]
	if !contains(affected.Needs, "unit") || !contains(affected.Needs, "acceptance") ||
		!contains(affected.Needs, "acceptance-nuget") ||
		!strings.Contains(affected.If, "profile == 'format'") {
		t.Error("affected coverage lacks selected same-run sources")
	}
	gate := workflow.Jobs["gate"]
	if gate.If != "always()" || !contains(gate.Needs, "impact") || !contains(gate.Needs, "docs") ||
		!contains(gate.Needs, "coverage") || !contains(gate.Needs, "affected-coverage") {
		t.Error("stable required gate is missing a profile dependency")
	}
	for _, name := range append(fullJobs, "unit", "lint", "acceptance", "acceptance-nuget", "terraform", "deploy-terraform") {
		if name != "acceptance-gradle" && !contains(gate.Needs, name) {
			t.Errorf("required gate does not await %s", name)
		}
	}
	if contains(gate.Needs, "acceptance-gradle") {
		t.Error("informational Gradle must not become required")
	}
	if len(gate.Steps) != 1 ||
		!strings.Contains(gate.Steps[0].Run, `require unselected "$result" skipped`) ||
		!strings.Contains(gate.Steps[0].Run, `require full-job "$result" success`) ||
		!strings.Contains(
			gate.Steps[0].Run,
			`require affected-coverage "$AFFECTED_COVERAGE" success`,
		) {
		t.Error("stable gate must distinguish skipped jobs from failed selected jobs")
	}
	impact := workflow.Jobs["impact"]
	if len(impact.Steps) == 0 {
		t.Fatal("impact planner is missing")
	}
	var releaseExpr string
	for _, step := range impact.Steps {
		if step.Name == "Select profile" {
			releaseExpr = step.Env["RELEASE_PR"]
		}
	}
	if !strings.Contains(
		releaseExpr,
		"github.event.pull_request.head.repo.full_name == github.repository",
	) ||
		!strings.Contains(
			releaseExpr,
			"github.event.pull_request.head.ref == 'release-please--branches--main'",
		) {
		t.Error("release selection must use same-repository branch identity, not PR title")
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
