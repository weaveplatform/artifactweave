package pkgmove

import (
	"bytes"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// splitDir holds the archives of the split-package tests, one per test,
// named after the test without its TestUnit prefix. They run as
// TestUnitScripts runs those in testdata; TestUnitScripts leaves them out.
const splitDir = "internal/devtools/pkgmove/testdata/split"

// runSplit runs the archive of the calling test.
func runSplit(t *testing.T) {
	t.Helper()
	setFixtureEnv(t)
	name := strings.TrimPrefix(t.Name(), "TestUnit")
	newScript(t, repopath.Path(t, splitDir+"/"+name+".txtar"), goVersion(t)).run()
}

func TestUnitMoveFilesMovesOnlyListedFiles(t *testing.T)    { runSplit(t) }
func TestUnitMoveFilesCarriesFuzzCorpus(t *testing.T)       { runSplit(t) }
func TestUnitMoveFilesMakesExternalTests(t *testing.T)      { runSplit(t) }
func TestUnitFixQualifiesSymbolsInMovedFiles(t *testing.T)  { runSplit(t) }
func TestUnitFixRewritesImporterSelectors(t *testing.T)     { runSplit(t) }
func TestUnitFixReportsUnexportedReferences(t *testing.T)   { runSplit(t) }
func TestUnitFixReportsSplitMethodSets(t *testing.T)        { runSplit(t) }
func TestUnitFixReportsSourceReadingTests(t *testing.T)     { runSplit(t) }
func TestUnitFixConvertsExternalTests(t *testing.T)         { runSplit(t) }
func TestUnitFixBridgesRewritesAliasUses(t *testing.T)      { runSplit(t) }
func TestUnitFixBridgesCoversBuildTaggedFiles(t *testing.T) { runSplit(t) }
func TestUnitOwnershipFollowsMovedRootTests(t *testing.T)   { runSplit(t) }

// TestUnitSplitArchivesHaveTests proves that every archive in the split
// directory runs: each names a test function of this file.
func TestUnitSplitArchivesHaveTests(t *testing.T) {
	archives, err := filepath.Glob(filepath.Join(repopath.Path(t, splitDir), "*.txtar"))
	if err != nil || len(archives) == 0 {
		t.Fatalf("no split archives: %v", err)
	}
	src := string(repopath.Read(t, "internal/devtools/pkgmove/split_test.go"))
	for _, a := range archives {
		name := strings.TrimSuffix(filepath.Base(a), ".txtar")
		if !strings.Contains(src, "\nfunc TestUnit"+name+"(t *testing.T)") {
			t.Errorf("%s has no test TestUnit%s", filepath.Base(a), name)
		}
	}
}

// splitTree returns a tree of files, tracked with the extra paths, which
// are tracked but gone from the disk.
func splitTree(t *testing.T, files map[string]string, gone ...string) *tree {
	t.Helper()
	tr := &tree{
		ctx:     t.Context(),
		root:    writeTree(t, files),
		module:  "example.com/m",
		tracked: map[string]bool{},
	}
	for _, f := range slices.Concat(slices.Collect(maps.Keys(files)), gone) {
		tr.files = append(tr.files, f)
		tr.tracked[f] = true
	}
	slices.Sort(tr.files)
	return tr
}

// TestUnitSplitStopsOnFilesItCannotRead proves that the file moves and fix
// stop with an error, rather than guess, at a tracked Go file that is gone
// from the disk or does not parse, wherever they read one.
func TestUnitSplitStopsOnFilesItCannotRead(t *testing.T) {
	tr := splitTree(t, map[string]string{
		"internal/a/a.go":       "package a\n",
		"internal/b/bad.go":     "packge b\n",
		"internal/c/ok.go":      "package c\n",
		"internal/d/ok.go":      "package d\n",
		"internal/d/ok_test.go": "package d\n",
		"internal/f/f_test.go":  "package f\n\nfunc {\n",
		"internal/t/t_test.go":  "packge t\n",
	}, "internal/a/gone.go", "internal/a/gone_test.go", "internal/c/gone.go", "internal/e/gone_test.go", makefilePath)
	carry := func(from, to string, names map[string]string) error {
		fc := &fileCarry{clauses: map[string]*clauseRename{}, names: names}
		return tr.carryGoFile(fc, &fileEntry{}, fileMove{from: from, to: to}, nil, nil)
	}
	for name, err := range map[string]error{
		"carry a gone file":                       carry("internal/a/gone.go", "migration/x/gone.go", map[string]string{}),
		"carry a file that does not parse":        carry("internal/b/bad.go", "migration/x/bad.go", map[string]string{}),
		"carry from a directory with a gone file": carry("internal/a/a.go", "migration/x/a.go", map[string]string{}),
		"carry into a directory with a gone file": carry("internal/d/ok.go", "internal/c/ok.go", map[string]string{}),
		"carry a test into a directory with a gone file": carry(
			"internal/d/ok_test.go", "internal/e/ok_test.go", map[string]string{"internal/e": "e"}),
		"declare a gone file":                                 tr.declare("internal/a/gone.go", map[string]decl{}, map[string]string{}),
		"declare a file that does not parse":                  tr.declare("internal/f/f_test.go", map[string]decl{}, map[string]string{}),
		"read the tests of a directory with a gone test":      second(tr.externalOnly("internal/e")),
		"read a test clause that does not parse":              second(tr.externalOnly("internal/t")),
		"look for fuzz targets in a gone test":                second(tr.fuzzLeft("internal/a", nil)),
		"look for fuzz targets in a test that does not parse": second(tr.fuzzLeft("internal/f", nil)),
		"follow fuzz targets in a gone Makefile": tr.followFuzzTargets(
			&change{}, []fileMove{{from: "internal/f/f_test.go", to: "migration/x/f_test.go"}}),
		"convert a gone test":                        second(tr.externalFiles([]string{"internal/a/gone_test.go"})),
		"convert a test whose clause does not parse": second(tr.externalFiles([]string{"internal/t/t_test.go"})),
		"index a directory with a gone file": second(tr.splitScope(
			[]fileMove{{from: "internal/x/x.go", to: "internal/a/x.go"}})),
		"index a gone moved file": second(tr.splitScope(
			[]fileMove{{from: "internal/q/q.go", to: "internal/r/r.go"}})),
		"read a file that no load compiled": tr.uncompiled(
			newFixPlan(), []string{"internal/a/gone.go"}, map[string]*typedFile{}, newWatch()),
		"read the sources of a directory with a gone file": tr.reportSources(newFixPlan(), []string{"internal/a"}),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := tr.reportSources(newFixPlan(), []string{"internal/f"}); err != nil {
		t.Errorf("a file that does not parse fails the source report: %v", err)
	}
	if err := os.WriteFile(
		tr.abs(makefilePath),
		[]byte("FUZZ_PKGS ?= ./internal/f\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := tr.followFuzzTargets(
		&change{},
		[]fileMove{{from: "internal/f/f_test.go", to: "migration/x/f_test.go"}},
	)
	if err == nil {
		t.Error("followFuzzTargets read past a test that does not parse")
	}
}

// second returns the error of a call that returns a value and an error.
func second[T any](_ T, err error) error { return err }

// TestUnitFixStopsOnAFileItCannotRewrite proves that fix stops at a planned
// file it cannot read, rewrite or write back, before it runs make fmt.
func TestUnitFixStopsOnAFileItCannotRewrite(t *testing.T) {
	tr := splitTree(t, map[string]string{"internal/a/a.go": "package a\n"})
	e := &env{ctx: t.Context(), dir: tr.root, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	for name, g := range map[string]*goFix{
		"a gone file":                  {path: "internal/a/gone.go"},
		"an edit that breaks the file": {path: "internal/a/a.go", es: edits{{start: 0, end: 7, text: "func"}}},
	} {
		plan := newFixPlan()
		plan.files[g.path] = g
		if err := e.finishFix(tr, plan, false, true); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only file")
	}
	if err := os.Chmod(tr.abs("internal/a/a.go"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tr.abs("internal/a/a.go"), 0o600) })
	plan := newFixPlan()
	plan.files["internal/a/a.go"] = &goFix{
		path: "internal/a/a.go",
		es:   edits{{start: 8, end: 9, text: "b"}},
	}
	err := e.finishFix(tr, plan, false, true)
	if err == nil || !strings.HasPrefix(err.Error(), "write ") {
		t.Errorf("fix of a read-only file: %v", err)
	}
}

func TestUnitFuzzTargets(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", `package x

import "testing"

func Fuzz(f *testing.F)              {}
func FuzzX(f *testing.F)             {}
func Fuzzy(f *testing.F)             {}
func FuzzByValue(f testing.F)        {}
func FuzzLocal(f *F)                 {}
func FuzzOther(f *other.F)           {}
func FuzzTwo(f, g *testing.F)        {}
func (r R) FuzzMethod(f *testing.F)  {}
func TestUnitX(f *testing.F)         {}
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fuzzTargets(f), []string{"Fuzz", "FuzzX"}; !slices.Equal(got, want) {
		t.Errorf("fuzzTargets = %q, want %q", got, want)
	}
}

func TestUnitEmbedPatterns(t *testing.T) {
	for args, want := range map[string][]string{
		" a b":          {"a", "b"},
		` "a b.txt"	c `: {"a b.txt", "c"},
		"`d e`":         {"d e"},
		"all:static":    {"all:static"},
	} {
		if got := embedPatterns(args); !slices.Equal(got, want) {
			t.Errorf("embedPatterns(%q) = %q, want %q", args, got, want)
		}
	}
	files := []string{"static/css/a.css", "b.txt"}
	for pattern, want := range map[string]bool{
		"static": true, "static/css": true, "*.txt": true, "missing": false, "static/*.js": false,
	} {
		if got := embedMatches(pattern, files); got != want {
			t.Errorf("embedMatches(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func TestUnitImportChain(t *testing.T) {
	graph := map[string][]string{"a": {"b"}, "b": {"c", "a"}, "c": {"d"}}
	for _, c := range []struct {
		from, to string
		want     []string
	}{
		{"b", "a", []string{"b", "a"}},
		{"a", "d", []string{"a", "b", "c", "d"}},
		{"d", "a", nil},
	} {
		if got := importChain(graph, c.from, c.to); !slices.Equal(got, c.want) {
			t.Errorf("importChain(%s, %s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestUnitIdentifier(t *testing.T) {
	for s, want := range map[string]string{"9go-lib.v2": "golibv2", "Kernel_X": "kernel_x", "ünï": "n"} {
		if got := identifier(s); got != want {
			t.Errorf("identifier(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestUnitBaseIdent(t *testing.T) {
	for src, want := range map[string]string{
		"T": "T", "p.T": "T", "*T": "T", "(T)": "T", "T[int]": "T", "*p.T[int, string]": "T",
		"[]T": "", "func()": "",
	} {
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if id := baseIdent(e); id != nil {
			got = id.Name
		}
		if got != want {
			t.Errorf("baseIdent(%s) = %q, want %q", src, got, want)
		}
	}
}

func TestUnitObjectKind(t *testing.T) {
	pkg := types.NewPackage("example.com/p", "p")
	recv := types.NewVar(token.NoPos, pkg, "r", types.Typ[types.Int])
	for want, obj := range map[string]types.Object{
		"method": types.NewFunc(token.NoPos, pkg, "m",
			types.NewSignatureType(recv, nil, nil, nil, nil, false)),
		"func":       types.NewFunc(token.NoPos, pkg, "f", types.NewSignatureType(nil, nil, nil, nil, nil, false)),
		"field":      types.NewField(token.NoPos, pkg, "x", types.Typ[types.Int], false),
		"var":        types.NewVar(token.NoPos, pkg, "v", types.Typ[types.Int]),
		"const":      types.NewConst(token.NoPos, pkg, "c", types.Typ[types.Int], constant.MakeInt64(1)),
		"type":       types.NewTypeName(token.NoPos, pkg, "T", nil),
		"identifier": types.NewLabel(token.NoPos, pkg, "l"),
	} {
		if got := objectKind(obj); got != want {
			t.Errorf("objectKind(%s) = %q, want %q", obj, got, want)
		}
	}
}
