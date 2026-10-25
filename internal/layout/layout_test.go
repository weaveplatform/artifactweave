// Package layout holds the import-tier test described in
// docs/implementation/repo-layout.md. It has no production code: the test is
// the enforcement, so a forbidden import fails the gate rather than being
// noticed in review.
package layout

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/weaveplatform/artifactweave"

// forbidden maps a directory prefix (relative to the module root) to the
// import prefixes its packages must not use. pkg/ is the public library and
// must stay importable without dragging in the server; format handlers must
// not reach back into the server so they stay testable through narrow
// interfaces.
var forbidden = map[string][]string{
	"pkg/":             {module + "/internal/", module + "/cmd/"},
	"internal/format/": {module + "/internal/server"},
}

func TestUnitImportTiers(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "cover" || d.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for dir, banned := range forbidden {
			if !strings.HasPrefix(rel, dir) {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return err
				}
				for _, b := range banned {
					if strings.HasPrefix(p, b) {
						violations = append(violations, rel+" imports "+p)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestUnitForbiddenRulesDetectViolation proves the walker sees imports at all,
// so a future refactor cannot silently turn the test into a no-op.
func TestUnitForbiddenRulesDetectViolation(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n\nimport _ \"" + module + "/internal/server\"\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(dir, "x.go"), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Imports) != 1 {
		t.Fatalf("expected one import, got %d", len(f.Imports))
	}
	p, err := strconv.Unquote(f.Imports[0].Path.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, forbidden["internal/format/"][0]) {
		t.Fatalf("rule did not match %q", p)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + dir)
		}
		dir = parent
	}
}
