package layout

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// updateLedger names the variable that makes TestUnitLedgerComplete write the
// canonical form, as ARTIFACTWEAVE_UPDATE_API_LEDGER does for the API ledger.
const updateLedger = "ARTIFACTWEAVE_UPDATE_LEDGER"

func TestUnitLedgerComplete(t *testing.T) {
	report, problems := checkLedger(repopath.Root(t), os.Getenv(updateLedger) == "1")
	for _, p := range problems {
		t.Error(p)
	}
	counts := report.Counts()
	for _, status := range slices.Sorted(maps.Keys(counts)) {
		t.Logf("%d %s", counts[status], status)
	}
}

// checkLedger returns every way migration/ledger.json under root falls short,
// with the statuses derived from the tree. With update it first rewrites the
// file in canonical form.
func checkLedger(root string, update bool) (ledger.Report, []string) {
	path := filepath.Join(root, "migration", "ledger.json")
	l, err := ledger.Load(path)
	if err != nil {
		return ledger.Report{}, []string{err.Error()}
	}
	var problems []string
	for _, err := range l.Validate() {
		problems = append(problems, err.Error())
	}

	canonical := ledger.Format(l)
	if update {
		if err := os.WriteFile(path, canonical, 0o644); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, canonical) {
		problems = append(
			problems,
			"migration/ledger.json is not in canonical form; run "+updateLedger+
				"=1 go test ./migration/layout -run '^TestUnitLedgerComplete$' and review the diff",
		)
	}

	dirs, err := repopath.GoDirs(root)
	if err != nil {
		problems = append(problems, err.Error())
	}
	entries := map[string]bool{}
	for _, p := range l.Packages {
		entries[p.From] = true
	}
	for _, dir := range dirs {
		if ledger.IsLegacy(dir) && !entries[dir] {
			problems = append(problems, dir+": legacy package directory has no ledger entry")
		}
	}

	report, err := l.Status(root)
	if err != nil {
		problems = append(problems, err.Error())
	}
	for _, p := range report.Packages {
		if p.Status == ledger.StatusBroken {
			problems = append(
				problems,
				p.From+": broken: neither the legacy directory nor the target "+
					p.To+" holds Go files",
			)
		}
	}
	for _, f := range report.Files {
		if f.Status == ledger.StatusBroken {
			problems = append(
				problems,
				f.From+": broken: neither the legacy path nor its target exists",
			)
		}
	}
	for _, p := range report.Placeholders {
		switch p.Status {
		case ledger.StatusMissing:
			problems = append(problems, "placeholder "+p.To+" is missing its doc.go")
		case ledger.StatusImplemented:
			problems = append(problems, "placeholder "+p.To+" holds Go files besides doc.go; "+
				"remove its ledger entry and rewrite its Status")
		}
	}
	return report, problems
}

func TestUnitLedgerCheckDetectsViolations(t *testing.T) {
	// A complete tree: one legacy package with its move pending, one
	// placeholder holding doc.go. Each case breaks one check.
	base := func() *ledger.Ledger {
		return &ledger.Ledger{
			Schema:      1,
			Module:      "github.com/weaveplatform/artifactweave",
			StagingRoot: "migration",
			FinalRoot:   "internal",
			Packages: []ledger.Package{
				{From: "internal/a", Action: ledger.ActionMove, To: "platform/a", Ticket: "PLT-03"},
			},
			Files: []ledger.File{
				{
					From:   "internal/a/a_test.go",
					Action: ledger.ActionMove,
					To:     "kernel",
					Ticket: "PLT-03",
				},
			},
			Placeholders: []ledger.Placeholder{{To: "platform/b", Phases: []string{"P16"}}},
		}
	}
	files := []string{
		"internal/a/a.go",
		"internal/a/a_test.go",
		"migration/platform/b/doc.go",
		"cmd/x/x.go",
	}

	for _, tc := range []struct {
		name   string
		change func(l *ledger.Ledger)
		files  []string
		raw    string
		want   string
	}{
		{name: "complete", files: files},
		{name: "unreadable ledger", files: files, raw: "{", want: "unexpected EOF"},
		{
			name: "invalid entry", files: files, change: func(l *ledger.Ledger) { l.Packages[0].Ticket = "PLT-3" },
			want: `ticket "PLT-3" does not match`,
		},
		{name: "not canonical", files: files, raw: "indent", want: "not in canonical form"},
		{
			name: "legacy directory without an entry", files: append(slices.Clone(files), "pkg/store/x/x.go"),
			want: "pkg/store/x: legacy package directory has no ledger entry",
		},
		{
			name: "broken package", files: []string{"internal/a/README.md", "migration/platform/b/doc.go"},
			change: func(l *ledger.Ledger) { l.Files = nil }, want: "internal/a: broken",
		},
		{
			name: "broken file", files: []string{"internal/a/a.go", "migration/platform/b/doc.go"},
			want: "internal/a/a_test.go: broken",
		},
		{name: "placeholder missing", files: files[:2], want: "placeholder platform/b is missing its doc.go"},
		{
			name: "placeholder implemented", files: append(slices.Clone(files), "migration/platform/b/b.go"),
			want: "placeholder platform/b holds Go files besides doc.go",
		},
		{
			name: "unreadable directory", files: append(slices.Clone(files[:2]), "migration/platform/b"),
			want: "derive the ledger status",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tc.files {
				write(t, filepath.Join(root, filepath.FromSlash(f)), "package x\n")
			}
			l := base()
			if tc.change != nil {
				tc.change(l)
			}
			content := string(ledger.Format(l))
			switch tc.raw {
			case "":
			case "indent":
				content = strings.ReplaceAll(content, "    ", "  ")
			default:
				content = tc.raw
			}
			write(t, filepath.Join(root, "migration", "ledger.json"), content)

			_, problems := checkLedger(root, false)
			if tc.want == "" && problems != nil {
				t.Fatalf("problems in a complete tree: %q", problems)
			}
			if tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.want)) {
				t.Fatalf("problems = %q; want one containing %q", problems, tc.want)
			}
		})
	}

	t.Run("update writes the canonical form", func(t *testing.T) {
		root := t.TempDir()
		for _, f := range files {
			write(t, filepath.Join(root, filepath.FromSlash(f)), "package x\n")
		}
		path := filepath.Join(root, "migration", "ledger.json")
		write(t, path, strings.ReplaceAll(string(ledger.Format(base())), "    ", "\t"))
		report, problems := checkLedger(root, true)
		if problems != nil {
			t.Fatalf("problems after the update: %q", problems)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, ledger.Format(base())) {
			t.Errorf("ledger after the update = %q, %v; want the canonical form", got, err)
		}
		if counts := report.Counts(); !maps.Equal(
			counts,
			map[string]int{ledger.StatusPending: 2, ledger.StatusOK: 1},
		) {
			t.Errorf("counts = %v", counts)
		}
	})
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
