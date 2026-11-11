package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func TestUnitTopLevelDirectories(t *testing.T) {
	for _, p := range checkTopLevel(repopath.Root(t)) {
		t.Error(p)
	}

	conforming := []string{
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
			name: "unknown top-level directory", add: "internal/artifact/doc.go",
			want: "internal/artifact: unknown top-level directory",
		},
		{
			name: "a file directly under internal/", add: "internal/README.md",
			want: "internal/README.md: no file may sit directly under internal/",
		},
		{
			name: "a Go file directly under internal/", add: "internal/doc.go",
			want: "internal/doc.go: no file may sit directly under internal/",
		},
		{
			name: "unknown pkg directory", add: "pkg/util/util.go",
			want: "pkg/util: pkg/ holds only client and containertest",
		},
		{name: "dot files are skipped", add: "internal/.DS_Store"},
		{name: "dot directories are skipped", add: "pkg/.cache/x.go"},
		{name: "no internal directory", drop: "internal", want: "internal: "},
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
		case !e.IsDir():
			report("%s/%s: no file may sit directly under %s/", root, name, root)
		}
	}

	entries, err = os.ReadDir(filepath.Join(dir, "pkg"))
	if err != nil {
		report("%v", err)
	}
	for _, e := range entries {
		p := "pkg/" + e.Name()
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") &&
			p != "pkg/client" && p != "pkg/containertest" {
			report("%s: pkg/ holds only client and containertest", p)
		}
	}
	return problems
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
