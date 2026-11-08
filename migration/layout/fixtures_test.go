package layout

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// rootRelative is the fix every violation names. A path counted from the
// package directory stops resolving when its package moves to another depth,
// and tools/pkgmove can rewrite a fixture path only when it is one
// root-relative literal.
const rootRelative = "name the path from the repository root through platform/testkit/repopath"

// fileCalls are the calls, by import path, whose first argument is a path the
// call opens, lists or resolves. A climbing literal passed to one reads by
// depth whether or not the path exists, so these fail without the existence
// check that other literals need.
var fileCalls = map[string][]string{
	"os":            {"ReadFile", "Open", "Stat", "Lstat", "ReadDir", "DirFS"},
	"path/filepath": {"Glob", "Abs", "WalkDir"},
}

// joinCalls are the Join functions, by import path, whose first argument
// fixes where the joined path starts.
var joinCalls = map[string][]string{"path": {"Join"}, "path/filepath": {"Join"}}

// exemption names a climbing literal in one test file by the file's
// root-relative path and the literal's value.
type exemption struct{ file, literal string }

// parentPrefix is the bare parent prefix, which always resolves: to the
// directory above the test. It is built from two literals because written as
// one it would resolve against this package too and fail the check it
// configures.
const parentPrefix = ".." + "/"

// traversalInputs excuses climbing literals that resolve to an existing
// repository path but are inputs a test feeds to a path check, never reads,
// each with its reason. An entry that excuses nothing is itself a violation,
// so a moved or rewritten test cannot leave its entry behind.
var traversalInputs = map[exemption]string{
	{"migration/artifacts/ecosystem/rpm/corruption_test.go", parentPrefix}: "prefixes a package location " +
		"that ReadPackage must reject",
	{"migration/artifacts/ecosystem/rpm/decode_test.go", parentPrefix}: "prefixes a package location that " +
		"the metadata decoder must reject",
	{"migration/artifacts/ecosystem/bower/http/archive_fault_test.go", parentPrefix}: "a tar directory entry " +
		"that climbs out of the archive, which the upload must reject",
	{"migration/artifacts/ecosystem/conda/http/shards_test.go", parentPrefix}: "a relative shard base URL that " +
		"climbs out of the channel, which shardURLs must reject",
	{"migration/artifacts/core/registry/registry_test.go", parentPrefix}: "prefixes an id that Status " +
		"must not resolve",
	{"migration/platform/blob/blob_test.go", parentPrefix}: "prefixes an upload id that " +
		"ValidUploadID must reject",
	{"test/acceptance/debian_steps_test.go", parentPrefix}: "the step refuses an expected " +
		"file name with this prefix",
	{"test/acceptance/pub_steps_test.go", parentPrefix}: "the step refuses a Dart project " +
		"directory with this prefix",
}

func TestUnitFixtureReadsAreRootRelative(t *testing.T) {
	for _, p := range checkFixtureReads(repopath.Root(t), traversalInputs) {
		t.Error(p)
	}
}

// checkFixtureReads returns, sorted, every depth-relative path in the _test.go
// files of the repository at root, walked with repopath.SkipDir, and every
// entry of exempt that excused nothing. tools/ is not walked: it stays where
// it is through the migration, so its paths never need rewriting.
func checkFixtureReads(root string, exempt map[exemption]string) []string {
	var problems []string
	used := map[exemption]bool{}
	fset := token.NewFileSet()
	err := fs.WalkDir(os.DirFS(root), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (rel == "tools" ||
			repopath.SkipDir(root, filepath.Join(root, filepath.FromSlash(rel)), d)):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(rel, "_test.go"):
			return nil
		}
		found, err := fixtureReads(fset, root, rel)
		if err != nil {
			problems = append(problems, err.Error())
		}
		for _, r := range found {
			key := exemption{rel, r.literal}
			if _, ok := exempt[key]; ok && r.existing {
				used[key] = true
				continue
			}
			problems = append(problems, r.text)
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}
	for key := range exempt {
		if !used[key] {
			problems = append(problems, fmt.Sprintf(
				"%s: the traversalInputs entry for %q excuses nothing; update or remove it",
				key.file, key.literal))
		}
	}
	slices.Sort(problems)
	return problems
}

// fixtureRead is a depth-relative path found in a test file. existing marks
// one found only because it resolves to an existing repository path, the one
// kind that a traversal input can be mistaken for.
type fixtureRead struct {
	text     string
	literal  string
	existing bool
}

// fixtureReads parses the test file at rel, a path relative to root, and
// returns its depth-relative paths. The AST is walked rather than the text so
// that a call split over several lines still counts.
func fixtureReads(fset *token.FileSet, root, rel string) ([]fixtureRead, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	imports := importNames(f)
	var found []fixtureRead
	report := func(lit *ast.BasicLit, value string, existing bool, format string, args ...any) {
		pos := fset.Position(lit.Pos())
		text := fmt.Sprintf(
			"%s:%d: %s; %s",
			pos.Filename,
			pos.Line,
			fmt.Sprintf(format, args...),
			rootRelative,
		)
		found = append(found, fixtureRead{text, value, existing})
	}
	// A literal a call already reported is not reported again as a path.
	reported := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.CallExpr:
			pkg, fn := calledFunc(n, imports)
			name := path.Base(pkg) + "." + fn
			switch lit, value := firstStringArg(n); {
			case lit == nil || !climbs(value):
			case slices.Contains(joinCalls[pkg], fn):
				reported[lit] = true
				report(
					lit,
					value,
					false,
					"%s starts at %q, so the path counts from the package directory",
					name,
					value,
				)
			case slices.Contains(fileCalls[pkg], fn):
				reported[lit] = true
				report(
					lit,
					value,
					false,
					"%s(%q) reads relative to the package directory",
					name,
					value,
				)
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING || reported[n] {
				return true
			}
			// A bare ".." is left out: it always resolves, and tests use it
			// as a path-traversal input dozens of times.
			value, err := strconv.Unquote(n.Value)
			if err != nil || value == ".." || !climbs(value) {
				return true
			}
			// A literal that leaves the repository or names nothing in it is
			// a path-traversal input, which tests use by the hundred.
			target := path.Join(path.Dir(rel), value)
			if climbs(target) {
				return true
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err == nil {
				if target == "." {
					target = "the repository root"
				}
				report(
					n,
					value,
					true,
					"%q names %s counted from the package directory",
					value,
					target,
				)
			}
		}
		return true
	})
	return found, nil
}

// climbs reports whether p, a slash-separated path, starts by leaving its
// directory: ".." itself or a path whose first element is "..".
func climbs(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return first == ".."
}

// importNames maps the names by which f refers to its imports to their import
// paths, so a call through an alias is still recognised.
func importNames(f *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(p)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = p
	}
	return names
}

// calledFunc returns the import path and name of the function call calls, or
// empty strings when it is not a function of a package f imports.
func calledFunc(call *ast.CallExpr, imports map[string]string) (pkg, fn string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || imports[x.Name] == "" {
		return "", ""
	}
	return imports[x.Name], sel.Sel.Name
}

// firstStringArg returns the first argument of call and its value when that
// argument is a string literal, or nil.
func firstStringArg(call *ast.CallExpr) (*ast.BasicLit, string) {
	if len(call.Args) == 0 {
		return nil, ""
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return nil, ""
	}
	return lit, value
}

func TestUnitFixtureReadRuleDetectsViolations(t *testing.T) {
	// testFile returns a test file whose test function runs body, which starts
	// on line 13. path/filepath is imported under another name, so every case
	// also proves that a call is matched by its import path.
	testFile := func(body string) string {
		return `package b

import (
	"os"
	"path"
	fp "path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

func TestUnitB(t *testing.T) {
` + body + "\n}\n"
	}
	// read is a violation of every rule, placed where the walk must not look.
	read := testFile(`	_ = fp.Join("..", "a")
	_, _ = os.ReadFile("../a/testdata/fixture.txt")
	_ = "../a/testdata/"`)
	conforming := map[string]string{
		"go.mod":                          "module github.com/weaveplatform/artifactweave\n",
		"internal/a/testdata/fixture.txt": "fixture\n",
		// Traversal inputs that name nothing in the repository, a ".." that
		// is not the first element, and a root-relative read all pass.
		"internal/a/a_test.go": testFile(
			`	_ = repopath.Read(t, "internal/a/testdata/fixture.txt")
	_ = fp.Join(t.TempDir(), "..", "x")
	for _, bad := range []string{"..", "../etc/passwd", "../../../outside", "a/../.."} {
		_, _ = os.Stat(path.Join("internal/a", bad))
	}`,
		),
		"internal/a/a.go":                      read,
		"internal/a/testdata/x_test.go":        read,
		"internal/nested/go.mod":               "module example.com/nested\n",
		"internal/nested/nested_test.go":       read,
		"tools/x/x_test.go":                    read,
		".worktrees/copy/internal/b/b_test.go": read,
	}
	const b = "internal/b/b_test.go"

	for _, tc := range []struct {
		name   string
		src    string // the content of b, or empty for no file
		exempt map[exemption]string
		want   []string // the problems expected, in sorted order, each by a part of its text
	}{
		{name: "conforming"},
		{
			name: "Join from the parent directory",
			src:  testFile(`	_ = fp.Join("..", "a", "testdata", "fixture.txt")`),
			want: []string{b + `:13: filepath.Join starts at ".."`},
		},
		{
			name: "Join split over several lines",
			src: testFile(`	_ = path.Join(
		"../a/testdata",
		"fixture.txt",
	)`),
			want: []string{b + `:14: path.Join starts at "../a/testdata"`},
		},
		{
			name: "file call on a path that does not exist",
			src:  testFile(`	_, _ = os.ReadFile("../a/testdata/missing.txt")`),
			want: []string{b + `:13: os.ReadFile("../a/testdata/missing.txt") reads relative`},
		},
		{
			name: "glob pattern",
			src:  testFile(`	_, _ = fp.Glob("../a/testdata/*.txt")`),
			want: []string{b + `:13: filepath.Glob("../a/testdata/*.txt") reads relative`},
		},
		{
			name: "variable holding an existing path",
			src: testFile(`	dir := "../a/testdata/"
	_ = dir`),
			want: []string{b + `:13: "../a/testdata/" names internal/a/testdata counted`},
		},
		{
			name: "existing path joined after another argument",
			src:  testFile(`	_ = fp.Join(t.TempDir(), "../a/testdata")`),
			want: []string{b + `:13: "../a/testdata" names internal/a/testdata counted`},
		},
		{
			name: "climb to the repository root",
			src:  testFile(`	_ = "../.."`),
			want: []string{b + `:13: "../.." names the repository root counted`},
		},
		{
			name:   "exempted traversal input",
			src:    testFile(`	_ = []string{"../a", "../a"}`),
			exempt: map[exemption]string{{b, "../a"}: "a traversal input"},
		},
		{
			name:   "exemption that excuses nothing",
			exempt: map[exemption]string{{b, "../a"}: "a traversal input"},
			want:   []string{b + `: the traversalInputs entry for "../a" excuses nothing`},
		},
		{
			name:   "exemption does not excuse a read",
			src:    testFile(`	_, _ = os.ReadFile("../a/testdata/fixture.txt")`),
			exempt: map[exemption]string{{b, "../a/testdata/fixture.txt"}: "a traversal input"},
			want: []string{
				b + `: the traversalInputs entry for "../a/testdata/fixture.txt" excuses nothing`,
				b + `:13: os.ReadFile("../a/testdata/fixture.txt") reads relative`,
			},
		},
		{
			name: "unparsable test file",
			src:  "packag b\n",
			want: []string{b + ":1:1: expected 'package'"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for f, content := range conforming {
				write(t, filepath.Join(root, filepath.FromSlash(f)), content)
			}
			if tc.src != "" {
				write(t, filepath.Join(root, filepath.FromSlash(b)), tc.src)
			}
			problems := checkFixtureReads(root, tc.exempt)
			if len(problems) != len(tc.want) {
				t.Fatalf("problems = %q; want %d, containing %q", problems, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(problems[i], want) {
					t.Errorf("problem %d = %q; want it to contain %q", i, problems[i], want)
				}
			}
		})
	}

	t.Run("missing root", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		if problems := checkFixtureReads(missing, nil); len(problems) != 1 {
			t.Errorf("problems = %q; want one for the missing root", problems)
		}
	})
}
