// citestselection selects the safe CI test profile for a pull request. Uncertain input
// always receives the full profile.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

type change struct {
	status string
	old    string
	path   string
}

type plan struct {
	profile   string
	reason    string
	mergeBase string
	changes   []change
	candidate affectedPlan
}

var commitID = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func main() {
	base := flag.String("base", "", "pull request base commit")
	head := flag.String("head", "", "pull request head commit")
	release := flag.Bool("release-pr", false, "same-repository release-please pull request")
	fullOverride := flag.Bool(
		"full-override",
		false,
		"explicit full qualification on this pull request",
	)
	output := flag.String("github-output", "", "GitHub Actions output file")
	summary := flag.String("summary", "", "GitHub Actions step summary file")
	coverageDir := flag.String(
		"check-coverage",
		"",
		"verify affected-package coverage in this merged covdata directory",
	)
	coveragePackages := flag.String("packages", "", "comma-separated changed production packages")
	selectionJSON := flag.String(
		"selection-json",
		"",
		"write selected Go test names and packages as JSON",
	)
	flag.Parse()
	if *coverageDir != "" {
		if err := checkAffectedCoverage(*coverageDir, *coveragePackages); err != nil {
			fatal(err)
		}
		return
	}

	p := selectPlan(*base, *head, *release)
	if *fullOverride {
		p = forceFull(p)
	}
	kinds, err := selectKinds(p)
	if err != nil {
		// Uncertain source inventory must never produce a narrow green check.
		// The full profile still fails visibly if its own inventory is broken.
		if p.profile != "full" {
			p.profile = "full"
			p.reason = "Test-kind selection failed; run full qualification: " + err.Error()
			p.candidate = affectedPlan{profile: "full", reason: p.reason}
			kinds, err = selectKinds(p)
		}
		if err != nil {
			fatal(err)
		}
	}
	if *selectionJSON != "" {
		if err := writeSelection(*selectionJSON, kinds); err != nil {
			fatal(err)
		}
	}
	provider, deployment := p.terraformLanes()
	if *output != "" {
		body := fmt.Sprintf(
			"profile=%s\nmerge_base=%s\nowner=%s\nunits=%s\ntags=%s\nclients=%s\ncoverpkg=%s\nchanged_packages=%s\nunit_packages=%s\nacceptance_packages=%s\nfuzz_packages=%s\nunit_run=%s\nacceptance_run=%s\nhas_unit=%t\nhas_acceptance=%t\nterraform_provider=%t\nterraform_deployment=%t\ncandidate_profile=%s\ncandidate_owner=%s\ncandidate_units=%s\ncandidate_tags=%s\ncandidate_clients=%s\n",
			p.profile,
			p.mergeBase,
			p.candidate.owner,
			strings.Join(p.candidate.units, " "),
			p.candidate.tags,
			strings.Join(p.candidate.clients, ","),
			p.candidate.coverpkg,
			strings.Join(p.candidate.changedPackages, ","),
			strings.Join(kinds.Unit, " "),
			strings.Join(kinds.Acc, " "),
			strings.Join(kinds.Fuzz, " "),
			kinds.runPattern(p.profile, "unit"),
			kinds.runPattern(p.profile, "acceptance"),
			kinds.hasUnit(),
			len(kinds.Acc) != 0,
			provider,
			deployment,
			p.candidate.profile,
			p.candidate.owner,
			strings.Join(p.candidate.units, " "),
			p.candidate.tags,
			strings.Join(p.candidate.clients, ","),
		)
		if err := appendFile(*output, body); err != nil {
			fatal(err)
		}
	}
	if *summary != "" {
		var body strings.Builder
		fmt.Fprintf(&body, "## CI test selection: %s\n\n%s\n\n", p.profile, p.reason)
		if *base != "" && *head != "" {
			fmt.Fprintf(
				&body,
				"Base `%s`; head `%s`; merge base `%s`.\n\n",
				*base,
				*head,
				p.mergeBase,
			)
		}
		if len(p.changes) > 0 {
			body.WriteString("Changed paths:\n\n")
			for _, c := range p.changes {
				fmt.Fprintf(&body, "- `%s` %q\n", c.status, c.path)
			}
		}
		fmt.Fprintf(
			&body,
			"\nAffected plan: **%s**. %s\n",
			p.candidate.profile,
			p.candidate.reason,
		)
		if p.candidate.owner != "" {
			fmt.Fprintf(
				&body,
				"\nOwner: `%s`; root server tests: `%t`; unit packages: `%s`; acceptance tags: `%s`; clients: `%s`.\n",
				p.candidate.owner,
				p.candidate.hasRootTests,
				strings.Join(p.candidate.units, " "),
				p.candidate.tags,
				strings.Join(p.candidate.clients, ","),
			)
		}
		fmt.Fprintf(
			&body,
			"\n| Lane | Selection | Reason |\n|---|---|---|\n"+
				"| Go unit | %s | %d `TestUnit` packages, %d fuzz seed packages |\n"+
				"| Go acceptance packages | %s | %d package `TestAcc` packages |\n"+
				"| Go acceptance native clients | %s | godog tags `%s` |\n"+
				"| Terraform provider conformance | %s | %s |\n"+
				"| Terraform deployment checks | %s | %s |\n",
			laneState(kinds.hasUnit()),
			len(kinds.Unit), len(kinds.Fuzz),
			laneState(len(kinds.Acc) != 0), len(kinds.Acc),
			laneState(p.profile == "full" || p.profile == "format"), p.nativeSummaryTags(),
			laneState(provider), p.terraformReason(true),
			laneState(deployment), p.terraformReason(false),
		)
		body.WriteString(
			"Exact selected Go entry points are retained in the test-selection artifact.\n",
		)
		if err := appendFile(*summary, body.String()); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("CI profile: %s (%s)\n", p.profile, p.reason)
	fmt.Printf("Affected candidate: %s (%s)\n", p.candidate.profile, p.candidate.reason)
}

func laneState(selected bool) string {
	if selected {
		return "selected"
	}
	return "skipped"
}

func (p plan) nativeSummaryTags() string {
	if p.profile == "full" {
		return "~@nuget&&~@gradle; @nuget&&~@gradle"
	}
	if p.profile == "format" {
		return p.candidate.tags
	}
	return "none"
}

func (p plan) terraformLanes() (provider, deployment bool) {
	if p.profile == "full" {
		return true, true
	}
	if p.profile == "deployment" {
		return p.candidate.shouldTestTerraformProviders, p.candidate.shouldTestTerraformDeployment
	}
	return false, false
}

func (p plan) terraformReason(provider bool) string {
	if p.profile == "full" {
		return "full qualification"
	}
	if p.profile != "deployment" {
		return "no Terraform inputs changed"
	}
	selectedProvider, selectedDeployment := p.terraformLanes()
	if provider && selectedProvider {
		return "JFrog provider fixture or shared Terraform tooling changed"
	}
	if !provider && selectedDeployment {
		return "deployment root or shared Terraform tooling changed"
	}
	return "this Terraform lane is unaffected"
}

func forceFull(p plan) plan {
	p.profile = "full"
	p.reason = "PR has the ci:full label; run full qualification."
	p.candidate = affectedPlan{profile: "full", reason: p.reason}
	return p
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func appendFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(value)
	return err
}

func selectPlan(base, head string, release bool) plan {
	if release {
		return plan{
			profile: "full",
			reason:  "Same-repository release-please branch requires full qualification.",
			candidate: affectedPlan{
				profile: "full",
				reason:  "Release candidate requires full qualification.",
			},
		}
	}
	if base == "" && head == "" {
		return plan{
			profile: "full",
			reason:  "Manual dispatch requires full qualification.",
			candidate: affectedPlan{
				profile: "full",
				reason:  "Manual dispatch requires full qualification.",
			},
		}
	}
	if !commitID.MatchString(base) || !commitID.MatchString(head) {
		return plan{
			profile:   "full",
			reason:    "Missing or invalid commit identity; selecting the full gate.",
			candidate: affectedPlan{profile: "full", reason: "Commit identity cannot be verified."},
		}
	}
	mergeBase, changes, err := changedPaths(base, head)
	if err != nil {
		return plan{
			profile:   "full",
			reason:    "Diff could not be resolved; selecting the full gate: " + err.Error(),
			candidate: affectedPlan{profile: "full", reason: "Diff could not be resolved."},
		}
	}
	p := classify(changes)
	p.mergeBase = mergeBase
	p.changes = changes
	p.candidate = proposeAffected(changes)
	if p.candidate.profile == "format" {
		dependents, err := reverseDependentUnits(p.candidate.changedPackages, p.candidate.owner)
		if err != nil {
			p.candidate = affectedPlan{profile: "full", reason: err.Error()}
		} else {
			p.candidate.units = append(p.candidate.units, dependents...)
			slices.Sort(p.candidate.units)
			p.candidate.units = slices.Compact(p.candidate.units)
		}
	}
	if p.profile == "full" && p.candidate.profile != "full" {
		p.profile = p.candidate.profile
		p.reason = p.candidate.reason
	}
	return p
}

func classify(changes []change) plan {
	if len(changes) == 0 {
		return plan{profile: "full", reason: "Empty diff; selecting the full gate."}
	}
	for _, c := range changes {
		if c.status == "" || !strings.ContainsRune("AMDRC", rune(c.status[0])) {
			return plan{profile: "full", reason: "Unknown change type; selecting the full gate."}
		}
		if isServerTest(c.path) || isServerTest(c.old) {
			if c.status[0] != 'M' {
				return plan{
					profile: "full",
					reason:  "New, removed or moved server test; selecting the full gate until its ownership is reviewed.",
				}
			}
			if _, ok := serverTestOwner(c.path); !ok {
				return plan{
					profile: "full",
					reason:  "Unmapped server test; selecting the full gate until its ownership is reviewed.",
				}
			}
		}
		if !documentationPath(c.path) || (c.old != "" && !documentationPath(c.old)) {
			return plan{
				profile: "full",
				reason:  "Code, configuration, fixture or unknown path changed; selecting the full gate.",
			}
		}
	}
	return plan{
		profile: "docs",
		reason:  "Every changed path is a documentation file; run documentation checks only.",
	}
}

func isServerTest(file string) bool {
	return strings.HasSuffix(file, "_test.go") &&
		(strings.HasPrefix(file, "internal/server/") || strings.HasPrefix(file, "test/server/"))
}

func documentationPath(path string) bool {
	if strings.HasPrefix(path, "docs/") {
		return strings.HasSuffix(path, ".md") ||
			(strings.HasPrefix(path, "docs/testing/evidence/") && strings.HasSuffix(path, ".json"))
	}
	if !strings.Contains(path, "/") {
		switch path {
		case "README.md", "CHANGELOG.md", "CONTRIBUTING.md", "CODE_OF_CONDUCT.md", "SECURITY.md":
			return true
		}
	}
	// Repository README files document the adjacent code or deployment. A
	// README inside testdata may be a fixture and must retain full coverage.
	return strings.HasSuffix(path, "/README.md") &&
		!strings.HasPrefix(path, "testdata/") &&
		!strings.Contains(path, "/testdata/")
}

func changedPaths(base, head string) (string, []change, error) {
	cmd := exec.Command("git", "merge-base", base, head)
	mergeOutput, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("git merge-base: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mergeOutput))
	if !commitID.MatchString(mergeBase) {
		return "", nil, fmt.Errorf("git merge-base returned an invalid commit")
	}
	cmd = exec.Command(
		"git",
		"diff",
		"--name-status",
		"-z",
		"--find-renames",
		mergeBase,
		head,
		"--",
	)
	data, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("git diff: %w", err)
	}
	changes, err := parseNameStatus(data)
	return mergeBase, changes, err
}

func parseNameStatus(data []byte) ([]change, error) {
	if len(data) == 0 {
		return nil, nil
	}
	fields := bytes.Split(data, []byte{0})
	if len(fields) < 2 || len(fields[len(fields)-1]) != 0 {
		return nil, fmt.Errorf("unterminated git diff output")
	}
	fields = fields[:len(fields)-1]
	var changes []change
	for i := 0; i < len(fields); {
		if i+1 >= len(fields) {
			return nil, fmt.Errorf("incomplete git diff output")
		}
		status := string(fields[i])
		if status == "" {
			return nil, fmt.Errorf("empty git diff status")
		}
		c := change{status: status, path: string(fields[i+1])}
		i += 2
		if status[0] == 'R' || status[0] == 'C' {
			if i >= len(fields) {
				return nil, fmt.Errorf("incomplete rename or copy")
			}
			c.old = c.path
			c.path = string(fields[i])
			i++
		}
		if c.path == "" || (c.old != "" && c.old == c.path) {
			return nil, fmt.Errorf("invalid git diff path")
		}
		changes = append(changes, c)
	}
	return changes, nil
}
