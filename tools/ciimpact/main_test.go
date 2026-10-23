package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		changes []change
		want    string
	}{
		{
			"docs only",
			[]change{{status: "M", path: "README.md"}, {status: "A", path: "docs/testing/ci.md"}},
			"docs",
		},
		{"deleted docs", []change{{status: "D", path: "docs/old.md"}}, "docs"},
		{
			"source rename into docs",
			[]change{{status: "R100", old: "internal/server/x.go", path: "docs/x.md"}},
			"full",
		},
		{
			"docs rename into source",
			[]change{{status: "R100", old: "docs/x.md", path: "internal/server/x.go"}},
			"full",
		},
		{"evidence json", []change{{status: "A", path: "docs/testing/evidence/run.json"}}, "docs"},
		{"other json", []change{{status: "M", path: "docs/implementation/plan.json"}}, "full"},
		{"unknown status", []change{{status: "T", path: "docs/a.md"}}, "full"},
		{
			"acceptance fixture",
			[]change{{status: "M", path: "test/acceptance/features/npm.feature"}},
			"full",
		},
		{"generated API", []change{{status: "M", path: "api/artifactory.openapi.yaml"}}, "full"},
		{"workflow", []change{{status: "M", path: ".github/workflows/quality-gate.yml"}}, "full"},
		{"empty", nil, "full"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.changes).profile; got != tc.want {
				t.Fatalf("profile = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectPlanFailsClosed(t *testing.T) {
	if got := selectPlan("", "", false).profile; got != "full" {
		t.Fatalf("manual dispatch profile = %q", got)
	}
	if got := selectPlan("", "", true).profile; got != "full" {
		t.Fatalf("release candidate profile = %q", got)
	}
	if got := selectPlan("not-a-commit", "also-not-a-commit", false).profile; got != "full" {
		t.Fatalf("invalid commit profile = %q", got)
	}
}

func TestParseNameStatus(t *testing.T) {
	changes, err := parseNameStatus(
		[]byte("M\x00docs/a.md\x00R100\x00docs/b.md\x00internal/b.go\x00D\x00docs/c.md\x00"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || changes[1].old != "docs/b.md" || changes[1].path != "internal/b.go" {
		t.Fatalf("changes = %#v", changes)
	}
	for _, malformed := range [][]byte{[]byte("M\x00docs/a.md"), []byte("R100\x00docs/a.md\x00")} {
		if _, err := parseNameStatus(malformed); err == nil {
			t.Fatalf("accepted malformed diff %q", malformed)
		}
	}
}

func TestChangedPathsUsesMergeBase(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "CI Test")
	git("config", "user.email", "ci@example.invalid")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "a.md"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-qb", "topic")
	git("mv", "docs/a.md", "docs/b.md")
	git("commit", "-qm", "rename")
	head := git("rev-parse", "HEAD")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	mergeBase, changes, err := changedPaths(base, head)
	if err != nil {
		t.Fatal(err)
	}
	if mergeBase != base || len(changes) != 1 || changes[0].old != "docs/a.md" ||
		changes[0].path != "docs/b.md" {
		t.Fatalf("merge base = %q, changes = %#v", mergeBase, changes)
	}
	if got := selectPlan(base, head, false).profile; got != "docs" {
		t.Fatalf("docs-only rename profile = %q", got)
	}
	if got := selectPlan(base, head, true).profile; got != "full" {
		t.Fatalf("release PR with docs-only diff profile = %q", got)
	}
	git("checkout", "-qb", "format", base)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "format", "npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "format", "npm", "handler.go"),
		[]byte("package npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "npm code")
	npmHead := git("rev-parse", "HEAD")
	selected := selectPlan(base, npmHead, false)
	if selected.profile != "full" || selected.candidate.profile != "format" ||
		selected.candidate.owner != "npm" {
		t.Fatalf("shadow format plan = %+v", selected)
	}
	if selected := selectPlan(base, npmHead, true); selected.candidate.profile != "full" {
		t.Fatalf("release format plan = %+v", selected.candidate)
	}
}
