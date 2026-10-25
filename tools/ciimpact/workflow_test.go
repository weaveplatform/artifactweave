package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUnitWorkflowKeepsOneRequiredGateForSelectedProfiles(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Name  string   `yaml:"name"`
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
	if !strings.Contains(string(data), "ready_for_review, labeled, unlabeled") {
		t.Error("label changes must rerun the required gate for ci:full override")
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
		"unit": "format", "lint": "format",
	} {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || !strings.Contains(job.If, "profile == 'full'") ||
			!strings.Contains(job.If, "profile == '"+other+"'") {
			t.Errorf("%s is not gated by full or %s", name, other)
		}
	}
	for name, flag := range map[string]string{
		"terraform": "terraform_provider", "deploy-terraform": "terraform_deployment",
	} {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") ||
			!strings.Contains(job.If, "needs.impact.outputs."+flag+" == 'true'") {
			t.Errorf("%s is not selected by its own Terraform impact flag", name)
		}
	}
	if job := workflow.Jobs["acceptance-go"]; !contains(job.Needs, "impact") ||
		!strings.Contains(job.If, "has_acceptance == 'true'") ||
		!strings.HasPrefix(job.Name, "Go acceptance tests") {
		t.Error("mixed-package TestAcc checks need a distinct selected Linux job")
	}
	if job := workflow.Jobs["unit"]; !strings.HasPrefix(job.Name, "Go unit tests") {
		t.Error("unit checks must expose their test kind")
	} else if !strings.Contains(job.If, "has_unit == 'true'") {
		t.Error("an acceptance-only format must skip the unit matrix explicitly")
	}
	if !strings.HasPrefix(workflow.Jobs["terraform"].Name, "Terraform provider conformance") ||
		!strings.HasPrefix(workflow.Jobs["deploy-terraform"].Name, "Terraform deployment checks") {
		t.Error("the two Terraform checks need distinct visible names")
	}
	for _, step := range workflow.Jobs["unit"].Steps {
		if step.Name == "Unit tests (race, shuffle, coverage)" {
			if _, overwritesDefault := step.Env["COVERPKG"]; overwritesDefault ||
				step.Env["FORMAT_COVERPKG"] == "" ||
				!strings.Contains(step.Run, `COVERPKG="$FORMAT_COVERPKG"`) {
				t.Error(
					"full unit job must retain whole-module coverage; format job sets scoped coverage",
				)
			}
		}
	}
	for _, name := range []string{"acceptance", "acceptance-conan", "acceptance-nuget"} {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || !strings.Contains(job.If, "profile == 'full'") ||
			!strings.Contains(job.If, "profile == 'format'") {
			t.Errorf("%s is not gated by full or format", name)
		}
	}
	conan := workflow.Jobs["acceptance-conan"]
	if !strings.Contains(conan.If, "owner == 'conan'") ||
		!strings.Contains(conan.Name, "Conan 1 and 2 clients") ||
		!contains(workflow.Jobs["coverage"].Needs, "acceptance-conan") ||
		!contains(workflow.Jobs["affected-coverage"].Needs, "acceptance-conan") {
		t.Error("Conan client proof must run separately and feed both coverage gates")
	}
	var clientProof, upload bool
	for _, step := range conan.Steps {
		if step.Name == "Conan 1 and 2 native protocol acceptance (coverage)" &&
			strings.Contains(step.Run, "make conan-accept") {
			clientProof = true
		}
		if step.Name == "Upload Conan native client coverage" {
			upload = true
		}
	}
	if !clientProof || !upload {
		t.Error("Conan client job must run both clients and upload coverage")
	}
	for _, step := range workflow.Jobs["acceptance"].Steps {
		if strings.Contains(step.Name, "Conan") || strings.Contains(step.Run, "make conan-accept") {
			t.Error("Conan client proof must not consume the shared acceptance time limit")
		}
	}
	for _, tc := range []struct {
		job, step string
		full      bool
	}{
		{"acceptance", "Acceptance features (coverage-instrumented binary)", true},
		{"acceptance-nuget", "NuGet acceptance features (coverage-instrumented binary)", false},
		{"acceptance-gradle", "Gradle features (informational)", false},
	} {
		found := false
		for _, step := range workflow.Jobs[tc.job].Steps {
			if step.Name != tc.step {
				continue
			}
			found = true
			if !strings.Contains(step.Run, `^TestAccFeatures\z`) {
				t.Errorf("%s runs unrelated shared-package TestAcc suites", tc.job)
			}
			if tc.full && (!strings.Contains(step.Run, `if [ "$PROFILE" = full ]`) ||
				!strings.Contains(step.Run, "make accept ACCEPT_TIMEOUT=30m\n")) {
				t.Error("full qualification must run the other native TestAcc entry points once")
			}
		}
		if !found {
			t.Errorf("%s is missing its selected native feature step", tc.job)
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
	if !contains(affected.Needs, "unit") || !contains(affected.Needs, "acceptance-go") ||
		!contains(affected.Needs, "acceptance") ||
		!contains(affected.Needs, "acceptance-conan") ||
		!contains(affected.Needs, "acceptance-nuget") ||
		!strings.Contains(affected.If, "profile == 'format'") {
		t.Error("affected coverage lacks selected same-run sources")
	}
	gate := workflow.Jobs["gate"]
	if gate.If != "always()" || !contains(gate.Needs, "impact") ||
		!contains(gate.Needs, "acceptance-go") ||
		!contains(gate.Needs, "acceptance-conan") ||
		!contains(gate.Needs, "docs") ||
		!contains(gate.Needs, "coverage") ||
		!contains(gate.Needs, "affected-coverage") {
		t.Error("stable required gate is missing a profile dependency")
	}
	for _, name := range append(fullJobs, "unit", "lint", "acceptance", "acceptance-conan", "acceptance-nuget", "terraform", "deploy-terraform") {
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
		!strings.Contains(gate.Steps[0].Run, `require go-acceptance "$GO_ACCEPTANCE" success`) ||
		!strings.Contains(gate.Steps[0].Run, `require go-acceptance "$GO_ACCEPTANCE" skipped`) ||
		!strings.Contains(gate.Steps[0].Run, `require acceptance-conan "$CONAN" success`) ||
		!strings.Contains(gate.Steps[0].Run, `require acceptance-conan "$CONAN" skipped`) ||
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
	var overrideExpr string
	for _, step := range impact.Steps {
		if step.Name == "Select profile" {
			releaseExpr = step.Env["RELEASE_PR"]
			overrideExpr = step.Env["FULL_OVERRIDE"]
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
	if !strings.Contains(overrideExpr, "github.event.pull_request.labels.*.name") ||
		!strings.Contains(overrideExpr, "ci:full") {
		t.Error("full override must use the current PR label")
	}
}

func TestUnitRequiredGateExecutesTheFourLanePolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Actions Bash gate is exercised on Linux and macOS")
	}
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["gate"].Steps
	if len(steps) != 1 || steps[0].Run == "" {
		t.Fatal("required gate shell step is missing")
	}
	base := map[string]string{
		"PROFILE": "docs", "IMPACT": "success", "DOCS": "skipped",
		"UNIT": "skipped", "HAS_UNIT": "false",
		"GO_ACCEPTANCE": "skipped", "HAS_ACCEPTANCE": "false",
		"LINT": "skipped", "VULN": "skipped", "BUILD": "skipped",
		"ACCEPTANCE": "skipped", "CONAN": "skipped", "NUGET": "skipped", "COVERAGE": "skipped",
		"AFFECTED_COVERAGE": "skipped", "PARITY": "skipped",
		"TERRAFORM": "skipped", "PROVIDER_SELECTED": "false", "OCI": "skipped",
		"DEPLOY": "skipped", "DEPLOY_SELECTED": "false",
	}
	for _, tc := range []struct {
		name      string
		overrides map[string]string
		pass      bool
	}{
		{"docs", map[string]string{"DOCS": "success"}, true},
		{"provider fixture only", map[string]string{
			"PROFILE": "deployment", "PROVIDER_SELECTED": "true", "TERRAFORM": "success",
		}, true},
		{"deployment root only", map[string]string{
			"PROFILE": "deployment", "DEPLOY_SELECTED": "true", "DEPLOY": "success",
		}, true},
		{"shared Terraform tooling", map[string]string{
			"PROFILE": "deployment", "PROVIDER_SELECTED": "true", "TERRAFORM": "success",
			"DEPLOY_SELECTED": "true", "DEPLOY": "success",
		}, true},
		{"release full gate", map[string]string{
			"PROFILE": "full", "UNIT": "success", "HAS_UNIT": "true",
			"GO_ACCEPTANCE":  "success",
			"HAS_ACCEPTANCE": "true", "LINT": "success", "VULN": "success",
			"BUILD": "success", "ACCEPTANCE": "success", "CONAN": "success", "NUGET": "success",
			"COVERAGE": "success", "PARITY": "success", "TERRAFORM": "success",
			"PROVIDER_SELECTED": "true", "OCI": "success", "DEPLOY": "success",
			"DEPLOY_SELECTED": "true",
		}, true},
		{"cancelled release package acceptance", map[string]string{
			"PROFILE": "full", "UNIT": "success", "HAS_UNIT": "true",
			"GO_ACCEPTANCE":  "cancelled",
			"HAS_ACCEPTANCE": "true", "LINT": "success", "VULN": "success",
			"BUILD": "success", "ACCEPTANCE": "success", "CONAN": "success", "NUGET": "success",
			"COVERAGE": "success", "PARITY": "success", "TERRAFORM": "success",
			"PROVIDER_SELECTED": "true", "OCI": "success", "DEPLOY": "success",
			"DEPLOY_SELECTED": "true",
		}, false},
		{"affected format without package TestAcc", map[string]string{
			"PROFILE": "format", "UNIT": "success", "HAS_UNIT": "true",
			"LINT":       "success",
			"ACCEPTANCE": "success", "AFFECTED_COVERAGE": "success",
		}, true},
		{"affected format with package TestAcc", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "GO_ACCEPTANCE": "success",
			"UNIT": "success", "HAS_UNIT": "true", "LINT": "success",
			"ACCEPTANCE":        "success",
			"AFFECTED_COVERAGE": "success",
		}, true},
		{"Conan format requires separate clients", map[string]string{
			"PROFILE": "format", "OWNER": "conan", "UNIT": "success", "HAS_UNIT": "true",
			"LINT": "success", "ACCEPTANCE": "success", "CONAN": "success",
			"AFFECTED_COVERAGE": "success",
		}, true},
		{"Conan format rejects cancelled client job", map[string]string{
			"PROFILE": "format", "OWNER": "conan", "UNIT": "success", "HAS_UNIT": "true",
			"LINT": "success", "ACCEPTANCE": "success", "CONAN": "cancelled",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"other format rejects unexpected Conan job", map[string]string{
			"PROFILE": "format", "UNIT": "success", "HAS_UNIT": "true",
			"LINT": "success", "ACCEPTANCE": "success", "CONAN": "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"missing selected package TestAcc", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "GO_ACCEPTANCE": "skipped",
			"UNIT": "success", "HAS_UNIT": "true", "LINT": "success",
			"ACCEPTANCE":        "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"acceptance-only format skips unit", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "GO_ACCEPTANCE": "success",
			"LINT": "success", "ACCEPTANCE": "success", "AFFECTED_COVERAGE": "success",
		}, true},
		{"acceptance-only format rejects unexpected unit", map[string]string{
			"PROFILE": "format", "UNIT": "success", "HAS_ACCEPTANCE": "true",
			"GO_ACCEPTANCE": "success", "LINT": "success", "ACCEPTANCE": "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"cancelled selected provider", map[string]string{
			"PROFILE": "deployment", "PROVIDER_SELECTED": "true", "TERRAFORM": "cancelled",
		}, false},
		{"unexpected provider on deployment only", map[string]string{
			"PROFILE": "deployment", "DEPLOY_SELECTED": "true", "DEPLOY": "success",
			"TERRAFORM": "success",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range base {
				values[key] = value
			}
			for key, value := range tc.overrides {
				values[key] = value
			}
			values["PATH"] = os.Getenv("PATH")
			values["GITHUB_STEP_SUMMARY"] = filepath.Join(t.TempDir(), "summary.md")
			env := make([]string, 0, len(values))
			for key, value := range values {
				env = append(env, key+"="+value)
			}
			cmd := exec.Command("bash", "-e", "-c", steps[0].Run)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("gate pass=%t, want %t: %v\n%s", err == nil, tc.pass, err, out)
			}
		})
	}
}

func TestUnitAffectedCoverageCountsOnlySelectedSameRunSources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Actions coverage step uses Linux Bash mapfile")
	}
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var countScript string
	for _, step := range workflow.Jobs["affected-coverage"].Steps {
		if step.Name == "Merge and enforce changed-package floor" {
			var ok bool
			countScript, _, ok = strings.Cut(step.Run, "joined=")
			if !ok {
				t.Fatal("affected coverage source count is missing")
			}
		}
	}
	if countScript == "" {
		t.Fatal("affected coverage merge step is missing")
	}
	for _, tc := range []struct {
		name       string
		owner      string
		unit, acc  bool
		omitSource bool
	}{
		{"unit and acceptance", "bower", true, true, false},
		{"unit only", "bower", true, false, false},
		{"acceptance only", "bower", false, true, false},
		{"native only", "bower", false, false, false},
		{"missing selected acceptance", "bower", false, true, true},
		{"Conan includes native client coverage", "conan", true, false, false},
		{"Conan rejects missing client coverage", "conan", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			names := []string{"cover-accept"}
			if tc.unit {
				names = append(names, "cover-unit-linux", "cover-unit-macos", "cover-unit-windows")
			}
			if tc.acc && !tc.omitSource {
				names = append(names, "cover-accept-go")
			}
			if tc.owner == "conan" && !tc.omitSource {
				names = append(names, "cover-conan")
			}
			for _, name := range names {
				if err := os.MkdirAll(filepath.Join(root, "cover", name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-e", "-c", countScript)
			cmd.Dir = root
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HAS_UNIT=" + boolText(tc.unit),
				"HAS_ACCEPTANCE=" + boolText(tc.acc),
				"OWNER=" + tc.owner,
			}
			out, err := cmd.CombinedOutput()
			if (err == nil) == tc.omitSource {
				t.Fatalf("coverage source count result: %v\n%s", err, out)
			}
		})
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
