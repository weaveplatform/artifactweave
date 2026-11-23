package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

// harnessGuards are the guard tests #1861 names, plus the descriptor
// provider's; each reads a deployment-profile input as a file.
var harnessGuards = []string{
	"TestUnitWebhookProviderInventoryIsRequired",
	"TestUnitProjectProviderScenarioIsRequired",
	"TestUnitCleanupProviderScenarioIsRequired",
	"TestUnitSingleReplicationProviderScenarioIsRequired",
	"TestUnitDescriptorProviderControlsAreRequired",
	"TestUnitPinnedTestCommandsNameExistingTests",
	"TestUnitDeployCheckInputsSelectTheDeploymentChecks",
}

func TestUnitDeploymentChangesRunTheHarnessGuardTests(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		units []string
	}{
		{"test/terraform/run.sh", []string{harnessGuardTests}},
		{"test/terraform/check-webhooks.py", []string{harnessGuardTests}},
		{"test/terraform/check-projects.py", []string{harnessGuardTests}},
		{"test/terraform/webhook-cases.json", []string{harnessGuardTests}},
		{"deploy/artifactweave/terraform/webhooks/main.tf", []string{harnessGuardTests}},
		{"deploy/artifactweave/terraform/projects/.terraform.lock.hcl", []string{harnessGuardTests}},
		{"deploy/platform/terraform/aws/cdn/Makefile", []string{harnessGuardTests}},
		{"deploy/artifactweave/terraform/repositories/README.md", []string{harnessGuardTests}},
		// The fixture server is vetted and linted by the Linux lint job on
		// every run; the Windows unit job cannot build it.
		{"test/terraform/testdata/server/main.go", []string{harnessGuardTests}},
		// A Go package outside testdata joins the scoped vet, lint and tests.
		{"test/terraform/probe/probe.go", []string{"./test/terraform/probe", harnessGuardTests}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			candidate := proposeAffected([]change{{status: "M", path: tc.path}})
			if candidate.profile != "deployment" || !slices.Equal(candidate.units, tc.units) ||
				candidate.coverpkg != modulePath+"/tools/citestselection" {
				t.Fatalf("plan = %+v, want deployment with units %v", candidate, tc.units)
			}
		})
	}

	p := plan{
		profile:   "deployment",
		candidate: proposeAffected([]change{{status: "M", path: "test/terraform/run.sh"}}),
	}
	kinds, err := scanKinds(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kinds.Unit, []string{harnessGuardTests}) || len(kinds.Acc) != 0 ||
		len(kinds.Fuzz) != 0 {
		t.Fatalf("deployment kinds = unit %v, acceptance %v, fuzz %v", kinds.Unit, kinds.Acc, kinds.Fuzz)
	}
	run := kinds.runPattern(p.profile, "unit")
	for _, name := range harnessGuards {
		if !strings.Contains(run, "|"+name+"|") && !strings.Contains(run, "("+name+"|") &&
			!strings.Contains(run, "|"+name+")") {
			t.Errorf("unit run pattern omits %s: %s", name, run)
		}
	}
	if kinds.runPattern(p.profile, "acceptance") != `^\z` {
		t.Error("a deployment run must not select package TestAcc entry points")
	}

	empty := plan{profile: "deployment", candidate: affectedPlan{units: []string{"./absent"}}}
	if _, err := scanKinds(root, empty); err == nil {
		t.Error("a deployment plan without guard tests must fail closed")
	}
}

// The selection above is only half the fix: the quality gate must also run
// the jobs it selects. This evaluates the workflow's own job conditions and
// lint arguments against the impact outputs the selector writes for a
// run.sh-only change.
func TestUnitWorkflowSchedulesHarnessGuardsForDeploymentChanges(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(root + "/.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Name string         `yaml:"name"`
				Run  string         `yaml:"run"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}

	p := plan{profile: "deployment"}
	p.candidate = proposeAffected([]change{{status: "M", path: "test/terraform/run.sh"}})
	p.reason = p.candidate.reason
	kinds, err := scanKinds(root, p)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{"needs.impact.result": "success"}
	for line := range strings.Lines(githubOutputs(p, kinds)) {
		key, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		outputs["needs.impact.outputs."+key] = value
	}

	scheduled := map[string]bool{
		"unit-linux": true, "unit-macos": true, "unit-windows": true, "lint": true,
		"terraform": true, "deploy-terraform": true,
		"docs": false, "acceptance-go": false, "acceptance": false, "sqlite-windows": false,
		"coverage": false, "affected-coverage": false, "vuln": false, "build": false,
	}
	for job, want := range scheduled {
		got, err := evalCondition(workflow.Jobs[job].If, outputs)
		if err != nil {
			t.Fatalf("%s: %v", job, err)
		}
		if got != want {
			t.Errorf("%s scheduled=%t for a run.sh-only change, want %t", job, got, want)
		}
	}

	var fixtureVet bool
	for _, step := range workflow.Jobs["lint"].Steps {
		if strings.TrimSpace(step.Run) == "go vet ./test/terraform/testdata/server" {
			fixtureVet = true
		}
		args, ok := step.With["args"].(string)
		if !ok {
			continue
		}
		for _, tc := range []struct {
			profile string
			want    []string
		}{
			{"deployment", []string{harnessGuardTests, "./test/terraform/testdata/server"}},
			{"full", []string{"./...", "./test/terraform/testdata/server"}},
		} {
			values := maps.Clone(outputs)
			values["needs.impact.outputs.profile"] = tc.profile
			got, err := expandExpressions(args, values)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !slices.Contains(strings.Fields(got), want) {
					t.Errorf("%s lint args %q omit %s", tc.profile, got, want)
				}
			}
		}
	}
	if !fixtureVet {
		t.Error("the lint job must vet the Terraform fixture server, which ./... skips")
	}
}

func expandExpressions(text string, values map[string]string) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(text, "${{")
		if start < 0 {
			out.WriteString(text)
			return out.String(), nil
		}
		end := strings.Index(text[start:], "}}")
		if end < 0 {
			return "", fmt.Errorf("unterminated expression in %q", text)
		}
		value, err := evalExpression(text[start+3:start+end], values)
		if err != nil {
			return "", err
		}
		out.WriteString(text[:start])
		out.WriteString(value.text())
		text = text[start+end+2:]
	}
}

func evalCondition(expr string, values map[string]string) (bool, error) {
	value, err := evalExpression(expr, values)
	return value.truthy(), err
}

// exprValue is an Actions expression value: a string or a boolean.
type exprValue struct {
	isBool bool
	b      bool
	s      string
}

func (v exprValue) truthy() bool {
	if v.isBool {
		return v.b
	}
	return v.s != ""
}

func (v exprValue) text() string {
	if v.isBool {
		return fmt.Sprint(v.b)
	}
	return v.s
}

// evalExpression covers the subset the quality gate's conditions use:
// string literals, needs.* references, always(), ==, !=, &&, || and
// parentheses, with && and || returning an operand as Actions does.
func evalExpression(expr string, values map[string]string) (exprValue, error) {
	e := &exprParser{src: expr, values: values}
	value, err := e.or()
	if err == nil {
		e.space()
		if e.pos != len(e.src) {
			err = fmt.Errorf("unparsed %q", e.src[e.pos:])
		}
	}
	return value, err
}

type exprParser struct {
	src    string
	pos    int
	values map[string]string
}

func (e *exprParser) space() {
	for e.pos < len(e.src) && unicode.IsSpace(rune(e.src[e.pos])) {
		e.pos++
	}
}

func (e *exprParser) take(token string) bool {
	e.space()
	if strings.HasPrefix(e.src[e.pos:], token) {
		e.pos += len(token)
		return true
	}
	return false
}

func (e *exprParser) or() (exprValue, error) {
	left, err := e.and()
	for err == nil && e.take("||") {
		var right exprValue
		right, err = e.and()
		if !left.truthy() {
			left = right
		}
	}
	return left, err
}

func (e *exprParser) and() (exprValue, error) {
	left, err := e.compare()
	for err == nil && e.take("&&") {
		var right exprValue
		right, err = e.compare()
		if left.truthy() {
			left = right
		}
	}
	return left, err
}

func (e *exprParser) compare() (exprValue, error) {
	left, err := e.primary()
	if err != nil {
		return left, err
	}
	for _, op := range []string{"==", "!="} {
		if e.take(op) {
			right, err := e.primary()
			equal := left.text() == right.text()
			return exprValue{isBool: true, b: equal == (op == "==")}, err
		}
	}
	return left, nil
}

func (e *exprParser) primary() (exprValue, error) {
	e.space()
	switch {
	case e.take("("):
		value, err := e.or()
		if err == nil && !e.take(")") {
			err = fmt.Errorf("missing ) in %q", e.src)
		}
		return value, err
	case e.take("always()"):
		return exprValue{isBool: true, b: true}, nil
	case e.take("'"):
		end := strings.IndexByte(e.src[e.pos:], '\'')
		if end < 0 {
			return exprValue{}, fmt.Errorf("unterminated string in %q", e.src)
		}
		value := e.src[e.pos : e.pos+end]
		e.pos += end + 1
		return exprValue{s: value}, nil
	}
	start := e.pos
	for e.pos < len(e.src) && (e.src[e.pos] == '.' || e.src[e.pos] == '_' ||
		e.src[e.pos] == '-' || unicode.IsLetter(rune(e.src[e.pos]))) {
		e.pos++
	}
	name := e.src[start:e.pos]
	value, ok := e.values[name]
	if !ok {
		return exprValue{}, fmt.Errorf("unknown reference %q in %q", name, e.src)
	}
	return exprValue{s: value}, nil
}
