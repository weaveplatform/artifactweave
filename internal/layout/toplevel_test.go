package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/devtools/ledger"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// rootFiles are the only files allowed directly under root; promotion deletes
// all three.
var rootFiles = []string{"README.md", "ledger.json", "ledger.schema.json"}

func TestUnitTopLevelDirectories(t *testing.T) {
	for _, p := range checkTopLevel(repopath.Root(t)) {
		t.Error(p)
	}

	conforming := []string{
		"migration/README.md",
		"migration/ledger.json",
		"migration/ledger.schema.json",
		"internal/artifacts/doc.go",
		"internal/estate/doc.go",
		"internal/layout/doc.go",
		"internal/platform/blob/doc.go",
		"pkg/README.md",
		"pkg/client/artifactory/doc.go",
		"pkg/containertest/doc.go",
	}
	for _, tc := range []struct {
		name string
		add  string // a file added to the conforming tree
		drop string // a top-level directory removed from it
		want string // the one problem expected, or empty for none
	}{
		{name: "conforming"},
		{
			name: "unknown top-level directory", add: "migration/artifact/doc.go",
			want: "migration/artifact: unknown top-level directory",
		},
		{
			name: "another file under the staging root", add: "migration/notes.md",
			want: "migration/notes.md: only README.md, ledger.json and ledger.schema.json",
		},
		{
			name: "a Go file under the staging root", add: "migration/doc.go",
			want: "migration/doc.go: only README.md, ledger.json and ledger.schema.json",
		},
		{
			name: "unknown pkg directory", add: "pkg/util/util.go",
			want: "pkg/util: pkg/ holds only client, containertest and the legacy packages",
		},
		{name: "dot files are skipped", add: "migration/.DS_Store"},
		{name: "dot directories are skipped", add: "pkg/.cache/x.go"},
		{name: "no staging root", drop: "migration", want: "migration: "},
		{name: "no pkg directory", drop: "pkg", want: "pkg: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range append(slices.Clone(conforming), tc.add) {
				if f != "" && (tc.drop == "" || !strings.HasPrefix(f, tc.drop+"/")) {
					write(t, filepath.Join(dir, filepath.FromSlash(f)), "package x\n")
				}
			}
			problems := checkTopLevel(dir)
			if tc.want == "" && problems != nil {
				t.Fatalf("problems in a conforming tree: %q", problems)
			}
			if tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.want)) {
				t.Fatalf("problems = %q; want one containing %q", problems, tc.want)
			}
		})
	}
}

// checkTopLevel returns every entry directly under root or pkg/ in the
// repository at dir that the layout does not allow. A name starting with "." is
// skipped, as the walk rule skips dot-directories: macOS writes an untracked
// .DS_Store into any directory Finder opens.
func checkTopLevel(dir string) []string {
	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	entries, err := os.ReadDir(filepath.Join(dir, root))
	if err != nil {
		report("%v", err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."):
		case e.IsDir() && !slices.Contains(modules, name) && !slices.Contains(otherTops, name):
			report("%s/%s: unknown top-level directory; decision 0044 fixes the set, "+
				"listed in modules and otherTops in internal/layout", root, name)
		case !e.IsDir() && !slices.Contains(rootFiles, name):
			report("%s/%s: only README.md, ledger.json and ledger.schema.json may sit "+
				"directly under %s/", root, name, root)
		}
	}

	entries, err = os.ReadDir(filepath.Join(dir, "pkg"))
	if err != nil {
		report("%v", err)
	}
	for _, e := range entries {
		p := "pkg/" + e.Name()
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") &&
			p != "pkg/client" && p != "pkg/containertest" && !ledger.IsLegacy(p) {
			report("%s: pkg/ holds only client, containertest and the legacy packages "+
				"that have not moved yet", p)
		}
	}
	return problems
}
