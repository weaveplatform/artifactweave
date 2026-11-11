package main

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// unitJobs are the quality gate's Go unit test jobs, one per runner OS.
var unitJobs = []string{"unit-linux", "unit-macos", "unit-windows"}

func TestUnitNativeImageFailureSurvivesUnwritableRunnerStatus(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the native Actions wrapper requires Linux setsid")
	}
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["acceptance"]
	var build, monitor string
	for _, step := range job.Steps {
		switch step.Name {
		case "Client images (background)":
			build = step.Run
		case "Acceptance features (coverage-instrumented binary)":
			monitor = step.Run
		}
	}
	if build == "" || monitor == "" {
		t.Fatal("native image builder or acceptance monitor is missing")
	}
	runnerTemp, bin := t.TempDir(), t.TempDir()
	status := filepath.Join(t.TempDir(), "client-images.status")
	// A directory at the old temporary filename makes publishing status fail,
	// just as exhausted runner storage did after the image build failed.
	if err := os.Mkdir(filepath.Join(runnerTemp, "client-images.status.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"docker": "#!/bin/sh\necho 'no space left on device' >&2\nexit 17\n",
		"make":   "#!/bin/sh\nexec sleep 2\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "RUNNER_TEMP="+runnerTemp,
		"CLIENT_IMAGES_STATUS="+status, "CLIENTS=node", "PROFILE=full",
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-ec", build)
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start image builder: %v\n%s", err, output)
	}
	for deadline := time.Now().Add(time.Second); ; {
		result, err := os.ReadFile(status)
		if err == nil {
			if string(result) != "17\n" {
				t.Fatalf("image build status = %q, want 17", result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("image build failure was not published: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cmd = exec.CommandContext(ctx, "bash", "-ec", monitor)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil ||
		!strings.Contains(string(output), "Native client image build failed (exit 17)") {
		t.Fatalf("monitor did not report the failed image promptly: %v\n%s", err, output)
	}
	if !strings.HasPrefix(job.Env["CLIENT_IMAGES_STATUS"], "/dev/shm/") {
		t.Error("image status must use memory-backed storage independently of runner disk")
	}
	for _, tc := range []struct {
		name        string
		buildStatus string
		acceptExit  string
		wantError   string
	}{
		{"images ready before acceptance", "0", "0", ""},
		{"image failure arrives before acceptance", "17", "0", "Native client image build failed (exit 17)"},
		{"acceptance failure after images are ready", "0", "23", "exit status 23"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(status); err != nil {
				t.Fatal(err)
			}
			observed := t.TempDir()
			// Publish the build result on the monitor's first wait. Probing
			// an acceptance PID before that result also records an early
			// launch, independently of when the child is scheduled.
			for name, script := range map[string]string{
				"make": "#!/bin/sh\nprintf started > \"$OBSERVED/started\"\nexit \"$ACCEPT_EXIT\"\n",
				"grep": "#!/bin/sh\ncat >/dev/null\n" +
					"if [ ! -f \"$CLIENT_IMAGES_STATUS\" ]; then touch \"$OBSERVED/premature\"; fi\nexit 1\n",
				"sleep": "#!/bin/sh\nprintf '%s\\n' \"$BUILD_STATUS\" > \"$CLIENT_IMAGES_STATUS.tmp\"\n" +
					"mv \"$CLIENT_IMAGES_STATUS.tmp\" \"$CLIENT_IMAGES_STATUS\"\n",
			} {
				if err := os.WriteFile(
					filepath.Join(bin, name),
					[]byte(script),
					0o700,
				); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-ec", monitor)
			cmd.Env = append(env, "OBSERVED="+observed, "BUILD_STATUS="+tc.buildStatus,
				"ACCEPT_EXIT="+tc.acceptExit)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("monitor did not finish promptly: %v\n%s", err, output)
			}
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("ready images prevented acceptance: %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output)+err.Error(), tc.wantError) {
				t.Fatalf("monitor did not preserve failure %q: %v\n%s", tc.wantError, err, output)
			}
			if _, err := os.Stat(filepath.Join(observed, "premature")); !os.IsNotExist(err) {
				t.Fatal("acceptance was launched before image builds completed")
			}
			_, startedErr := os.Stat(filepath.Join(observed, "started"))
			if tc.buildStatus == "0" && startedErr != nil {
				t.Fatalf("acceptance did not run after successful image builds: %v", startedErr)
			}
			if tc.buildStatus != "0" && !os.IsNotExist(startedErr) {
				t.Fatal("acceptance ran after image builds failed")
			}
		})
	}
}

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
	fullJobs := []string{
		"vuln",
		"build",
		"acceptance-gradle",
		"acceptance-jvm",
		"oci-conformance",
		"parity",
	}
	for _, name := range fullJobs {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || job.If != "needs.impact.outputs.profile == 'full'" {
			t.Errorf("%s is not gated by the full impact profile", name)
		}
	}
	for _, name := range append([]string{"lint"}, unitJobs...) {
		job := workflow.Jobs[name]
		if !contains(job.Needs, "impact") || !strings.Contains(job.If, "profile == 'full'") ||
			!strings.Contains(job.If, "profile == 'format'") {
			t.Errorf("%s is not gated by full or format", name)
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
	for _, name := range unitJobs {
		job := workflow.Jobs[name]
		if !strings.HasPrefix(job.Name, "Go unit tests") {
			t.Errorf("%s: unit checks must expose their test kind", name)
		} else if !strings.Contains(job.If, "has_unit == 'true'") {
			t.Errorf("%s: an acceptance-only format must skip the unit jobs explicitly", name)
		}
		for _, step := range job.Steps {
			if step.Name == "Unit tests (race, shuffle, coverage)" {
				if _, overwritesDefault := step.Env["COVERPKG"]; overwritesDefault ||
					step.Env["FORMAT_COVERPKG"] == "" ||
					!strings.Contains(step.Run, `COVERPKG="$FORMAT_COVERPKG"`) {
					t.Errorf(
						"%s: full unit job must retain whole-module coverage; format job sets scoped coverage",
						name,
					)
				}
			}
		}
	}
	if !strings.HasPrefix(workflow.Jobs["terraform"].Name, "Terraform provider conformance") ||
		!strings.HasPrefix(workflow.Jobs["deploy-terraform"].Name, "Terraform deployment checks") {
		t.Error("the two Terraform checks need distinct visible names")
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
		{"acceptance-jvm", "JVM recipe features (coverage-instrumented binary)", false},
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
				!strings.Contains(step.Run, "setsid make accept ACCEPT_TIMEOUT=35m &\n") ||
				!strings.Contains(step.Run, `wait "$accept_pid"`)) {
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
	if !contains(affected.Needs, "acceptance-go") ||
		!contains(affected.Needs, "acceptance") ||
		!contains(affected.Needs, "acceptance-conan") ||
		!contains(affected.Needs, "acceptance-nuget") ||
		!strings.Contains(affected.If, "profile == 'format'") {
		t.Error("affected coverage lacks selected same-run sources")
	}
	for _, name := range unitJobs {
		for _, consumer := range []string{"coverage", "affected-coverage"} {
			if !contains(workflow.Jobs[consumer].Needs, name) {
				t.Errorf("%s does not wait for the %s coverage it merges", consumer, name)
			}
		}
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
	required := slices.Concat(fullJobs, unitJobs, []string{
		"lint", "acceptance", "acceptance-conan", "acceptance-nuget",
		"terraform", "deploy-terraform",
	})
	for _, name := range required {
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

// When the concurrency group cancels a run before its jobs start, each job
// reports a cancelled check under its literal name, and only a check of the
// same name from the run that replaces it clears it from the pull request. A
// matrix job, or a name built from an expression, has a literal name that no
// replacing check shares.
func TestUnitWorkflowCheckNamesSurviveACancelledRun(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name     string   `yaml:"name"`
			RunsOn   any      `yaml:"runs-on"`
			Needs    []string `yaml:"needs"`
			If       string   `yaml:"if"`
			Strategy struct {
				Matrix any `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for id, job := range workflow.Jobs {
		if job.Name == "" || strings.Contains(job.Name, "${{") {
			t.Errorf("%s: job name %q is not a fixed string", id, job.Name)
		}
		if job.Strategy.Matrix != nil {
			t.Errorf(
				"%s: a matrix job cancelled before it expands keeps a check no later run replaces",
				id,
			)
		}
	}
	first := workflow.Jobs[unitJobs[0]]
	runners := map[string]bool{}
	artifacts := map[string]bool{}
	for _, id := range unitJobs {
		job, ok := workflow.Jobs[id]
		if !ok {
			t.Fatalf("unit job %s is missing", id)
		}
		runner, _ := job.RunsOn.(string)
		if runner == "" || runners[runner] {
			t.Errorf("%s: runs-on %v must name a runner of its own", id, job.RunsOn)
		}
		runners[runner] = true
		if want := "Go unit tests (" + runner + ")"; job.Name != want {
			t.Errorf("%s: name = %q, want %q", id, job.Name, want)
		}
		if job.If != first.If || !reflect.DeepEqual(job.Needs, first.Needs) ||
			!reflect.DeepEqual(job.Steps, first.Steps) {
			t.Errorf("%s: the unit jobs must share one condition and one step list", id)
		}
		for _, step := range job.Steps {
			uses, _ := step["uses"].(string)
			if !strings.HasPrefix(uses, "actions/upload-artifact@") {
				continue
			}
			with, _ := step["with"].(map[string]any)
			name, _ := with["name"].(string)
			name = strings.ReplaceAll(name, "${{ github.job }}", id)
			if !strings.HasPrefix(name, "cover-unit-") || strings.Contains(name, "${{") ||
				artifacts[name] {
				t.Errorf("%s: coverage artifact %q needs a cover-unit- name of its own", id, name)
			}
			artifacts[name] = true
		}
	}
	if len(artifacts) != len(unitJobs) {
		t.Errorf("unit coverage artifacts = %d, want one per unit job", len(artifacts))
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
		"PROFILE": "docs", "IMPACT": "success", "DOCS": "skipped", "HAS_UNIT": "false",
		"UNIT_LINUX": "skipped", "UNIT_MACOS": "skipped", "UNIT_WINDOWS": "skipped",
		"GO_ACCEPTANCE": "skipped", "HAS_ACCEPTANCE": "false",
		"LINT": "skipped", "VULN": "skipped", "BUILD": "skipped",
		"ACCEPTANCE": "skipped", "CONAN": "skipped", "NUGET": "skipped", "COVERAGE": "skipped",
		"AFFECTED_COVERAGE": "skipped", "PARITY": "skipped", "JVM": "skipped",
		"TERRAFORM": "skipped", "PROVIDER_SELECTED": "false", "OCI": "skipped",
		"DEPLOY": "skipped", "DEPLOY_SELECTED": "false",
	}
	full := map[string]string{
		"PROFILE": "full", "HAS_UNIT": "true", "GO_ACCEPTANCE": "success",
		"HAS_ACCEPTANCE": "true", "LINT": "success", "VULN": "success",
		"BUILD": "success", "ACCEPTANCE": "success", "CONAN": "success", "NUGET": "success",
		"JVM":      "success",
		"COVERAGE": "success", "PARITY": "success", "TERRAFORM": "success",
		"PROVIDER_SELECTED": "true", "OCI": "success", "DEPLOY": "success",
		"DEPLOY_SELECTED": "true",
	}
	fullWith := func(key, value string) map[string]string {
		values := maps.Clone(full)
		values[key] = value
		return values
	}
	for _, tc := range []struct {
		name string
		// unit is the result of every unit job; an override can then set
		// one OS's job apart from the other two.
		unit      string
		overrides map[string]string
		pass      bool
	}{
		{"docs", "", map[string]string{"DOCS": "success"}, true},
		{"docs rejects an unexpected unit job", "", map[string]string{
			"DOCS": "success", "UNIT_WINDOWS": "success",
		}, false},
		{"provider fixture only", "", map[string]string{
			"PROFILE": "deployment", "PROVIDER_SELECTED": "true", "TERRAFORM": "success",
		}, true},
		{"deployment root only", "", map[string]string{
			"PROFILE": "deployment", "DEPLOY_SELECTED": "true", "DEPLOY": "success",
		}, true},
		{"deployment rejects an unexpected unit job", "", map[string]string{
			"PROFILE": "deployment", "DEPLOY_SELECTED": "true", "DEPLOY": "success",
			"UNIT_MACOS": "success",
		}, false},
		{"shared Terraform tooling", "", map[string]string{
			"PROFILE": "deployment", "PROVIDER_SELECTED": "true", "TERRAFORM": "success",
			"DEPLOY_SELECTED": "true", "DEPLOY": "success",
		}, true},
		{"release full gate", "success", full, true},
		{
			"cancelled release package acceptance", "success",
			fullWith("GO_ACCEPTANCE", "cancelled"), false,
		},
		{
			"release full gate rejects one cancelled unit job", "success",
			fullWith("UNIT_WINDOWS", "cancelled"), false,
		},
		{
			"release full gate rejects one skipped unit job", "success",
			fullWith("UNIT_LINUX", "skipped"), false,
		},
		{"affected format without package TestAcc", "success", map[string]string{
			"PROFILE": "format", "HAS_UNIT": "true",
			"LINT":       "success",
			"ACCEPTANCE": "success", "AFFECTED_COVERAGE": "success",
		}, true},
		{"affected format rejects one failed unit job", "success", map[string]string{
			"PROFILE": "format", "HAS_UNIT": "true", "UNIT_MACOS": "failure",
			"LINT":       "success",
			"ACCEPTANCE": "success", "AFFECTED_COVERAGE": "success",
		}, false},
		{"affected format with package TestAcc", "success", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "GO_ACCEPTANCE": "success",
			"HAS_UNIT": "true", "LINT": "success",
			"ACCEPTANCE":        "success",
			"AFFECTED_COVERAGE": "success",
		}, true},
		{"Conan format requires separate clients", "success", map[string]string{
			"PROFILE": "format", "OWNER": "conan", "HAS_UNIT": "true",
			"LINT": "success", "ACCEPTANCE": "success", "CONAN": "success",
			"AFFECTED_COVERAGE": "success",
		}, true},
		{"Conan format rejects cancelled client job", "success", map[string]string{
			"PROFILE": "format", "OWNER": "conan", "HAS_UNIT": "true",
			"LINT": "success", "ACCEPTANCE": "success", "CONAN": "cancelled",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"other format rejects unexpected Conan job", "success", map[string]string{
			"PROFILE": "format", "HAS_UNIT": "true",
			"LINT": "success", "ACCEPTANCE": "success", "CONAN": "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"missing selected package TestAcc", "success", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "GO_ACCEPTANCE": "skipped",
			"HAS_UNIT": "true", "LINT": "success",
			"ACCEPTANCE":        "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"acceptance-only format skips unit", "", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "GO_ACCEPTANCE": "success",
			"LINT": "success", "ACCEPTANCE": "success", "AFFECTED_COVERAGE": "success",
		}, true},
		{"acceptance-only format rejects unexpected unit", "success", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true",
			"GO_ACCEPTANCE": "success", "LINT": "success", "ACCEPTANCE": "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"acceptance-only format rejects one unexpected unit job", "", map[string]string{
			"PROFILE": "format", "HAS_ACCEPTANCE": "true", "UNIT_LINUX": "success",
			"GO_ACCEPTANCE": "success", "LINT": "success", "ACCEPTANCE": "success",
			"AFFECTED_COVERAGE": "success",
		}, false},
		{"cancelled selected provider", "", map[string]string{
			"PROFILE": "deployment", "PROVIDER_SELECTED": "true", "TERRAFORM": "cancelled",
		}, false},
		{"unexpected provider on deployment only", "", map[string]string{
			"PROFILE": "deployment", "DEPLOY_SELECTED": "true", "DEPLOY": "success",
			"TERRAFORM": "success",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range base {
				values[key] = value
			}
			if tc.unit != "" {
				for _, key := range []string{"UNIT_LINUX", "UNIT_MACOS", "UNIT_WINDOWS"} {
					values[key] = tc.unit
				}
			}
			for key, value := range tc.overrides {
				values[key] = value
			}
			if tc.name == "release full gate" {
				for _, result := range []string{"failure", "cancelled", "skipped", ""} {
					values["JVM"] = result
					env := []string{"PATH=" + os.Getenv("PATH")}
					for key, value := range values {
						env = append(env, key+"="+value)
					}
					cmd := exec.Command("bash", "-e", "-c", steps[0].Run)
					cmd.Env = env
					out, err := cmd.CombinedOutput()
					if err == nil || !strings.Contains(string(out), "full-job: expected success") {
						t.Fatalf(
							"full gate accepted or misclassified JVM result %q: %v\n%s",
							result,
							err,
							out,
						)
					}
				}
				values["JVM"] = "success"
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
