package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These guards turn path couplings that fail silently into test failures:
// go test passes when -run selects nothing, and git applies no attributes
// through a pattern that matches no file.

var (
	// The leading delimiter keeps a path relative to another directory, such
	// as ../internal/x, from matching.
	packageArgument = regexp.MustCompile(
		`(?:^|[\s'"=,])(\./(?:api|cmd|internal|migration|pkg|test|tools)(?:/[A-Za-z0-9_-]+)*)(/\.\.\.)?/?`,
	)
	modulePackageArgument = regexp.MustCompile(
		`\$\(MODULE\)/((?:internal|migration|pkg)(?:/[A-Za-z0-9_-]+)*)(/\.\.\.)?`,
	)
	runArgument = regexp.MustCompile(`(?:^|\s)-run(?:=|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"]+))`)
	// A pattern built from a make or shell variable is known only at run time.
	runVariable    = regexp.MustCompile(`\$[({A-Za-z_]`)
	conformanceVar = regexp.MustCompile(`^(CONFORMANCE_RUN|CONFORMANCE_PKG)\s*[:?]?=\s*(.*?)\s*$`)
	fuzzPkgsVar    = regexp.MustCompile(`^FUZZ_PKGS\s*[:?]?=(.*)$`)
)

type pinnedLine struct {
	file string
	line int
	text string
}

type packageRef struct {
	dir       string // slash-separated, relative to the repository root
	recursive bool   // the argument ends in /...
}

func TestUnitPinnedTestCommandsNameExistingTests(t *testing.T) {
	root := filepath.Clean("../..")
	var files []string
	for _, pattern := range []string{
		".github/workflows/*.yml",
		"Makefile",
		"deploy/platform/terraform/*/*/Makefile",
	} {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			t.Fatalf("%s matches no file", pattern)
		}
		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, filepath.ToSlash(rel))
		}
	}
	problems, err := pinnedCommandProblems(root, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

func TestUnitPinnedTestCommandGuardReportsEachBreak(t *testing.T) {
	root := t.TempDir()
	write := func(path, source string) {
		t.Helper()
		file := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/a/a_test.go", "package a\nimport \"testing\"\n"+
		"func TestAccA(t *testing.T) {}\nvar fixture = `\nfunc TestAccFixture(t *testing.T) {}\n`\n")
	write("internal/b/README", "")
	write("Makefile", strings.Join([]string{
		"ok: ; go test ./internal/a -run '^TestAccA$$/sub' -count=1",
		"joined: ; go test -run 'TestAccGone' \\\r",
		"  ./internal/a\r",
		"other: ; ./test/oci/run.sh -X $(MODULE)/internal/a.version=1",
		"variable: ; go test -run '$(RUN)' ./internal/a",
		"missing: ; go test ./internal/gone -run '^TestAccA'",
		"empty: ; go test ./internal/b",
		"renamed: ; go test ./internal/a/... -run 'TestAccA|TestAccFixture'",
	}, "\n"))
	write("deploy/Makefile", "CONFORMANCE_RUN := TestAccA\n")
	problems, err := pinnedCommandProblems(root, []string{"Makefile", "deploy/Makefile"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Makefile:2: -run "TestAccGone": "TestAccGone" selects no Test or Fuzz function in ./internal/a`,
		"Makefile:6: ./internal/gone is not a directory",
		"Makefile:7: ./internal/b holds no .go files",
		`Makefile:8: -run "TestAccA|TestAccFixture": "TestAccFixture" selects no Test or Fuzz function in ./internal/a/...`,
		"deploy/Makefile:1: CONFORMANCE_RUN is set without CONFORMANCE_PKG",
	}
	if !slices.Equal(problems, want) {
		t.Fatalf("problems:\n%s\nwant:\n%s", strings.Join(problems, "\n"), strings.Join(want, "\n"))
	}
}

func TestUnitGitattributesPathsExist(t *testing.T) {
	root := filepath.Clean("../..")
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") ||
			!strings.Contains(fields[0], "/") {
			continue
		}
		// git matches a pattern that contains a slash from the repository
		// root, as filepath.Glob does here.
		pattern := filepath.FromSlash(strings.TrimPrefix(fields[0], "/"))
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Errorf(".gitattributes:%d: %s: %v", i+1, fields[0], err)
		} else if len(matches) == 0 {
			t.Errorf(".gitattributes:%d: %s matches no file", i+1, fields[0])
		}
	}
}

// make test runs the seed corpora of FUZZ_PKGS only; CI passes its own list
// from the scan, so a package missing here is fuzzed in CI but never locally.
func TestUnitFuzzPkgsListsEveryFuzzTarget(t *testing.T) {
	root := filepath.Clean("../..")
	declared := map[string]bool{}
	for _, area := range scanRoots {
		base := filepath.Join(root, area)
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := walkTestSources(base, func(file string, decls []*ast.FuncDecl) error {
			if !slices.ContainsFunc(decls, isFuzzTarget) {
				return nil
			}
			dir, err := filepath.Rel(root, filepath.Dir(file))
			if err != nil {
				return err
			}
			declared["./"+filepath.ToSlash(dir)] = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, line := range strings.Split(string(data), "\n") {
		if m := fuzzPkgsVar.FindStringSubmatch(strings.TrimSuffix(line, "\r")); m != nil {
			listed = strings.Fields(m[1])
		}
	}
	for pkg := range declared {
		if !slices.Contains(listed, pkg) {
			t.Errorf("%s declares a fuzz target but is missing from FUZZ_PKGS", pkg)
		}
	}
	for _, pkg := range listed {
		if !declared[pkg] {
			t.Errorf("FUZZ_PKGS lists %s, which declares no fuzz target", pkg)
		}
	}
}

// pinnedGuard collects the problems of the commands pinned in a set of
// files; names caches each package's test functions, since several commands
// name the same large package.
type pinnedGuard struct {
	root     string
	names    map[packageRef][]string
	problems []string
}

// pinnedCommandProblems checks every package argument and -run pattern in
// files, which are slash-separated and relative to root.
func pinnedCommandProblems(root string, files []string) ([]string, error) {
	g := &pinnedGuard{root: root, names: map[packageRef][]string{}}
	for _, rel := range files {
		lines, err := readPinnedLines(root, rel)
		if err != nil {
			return nil, err
		}
		var conformanceRun, conformancePkg *pinnedLine
		for _, line := range lines {
			refs := packageRefs(line.text)
			for _, ref := range refs {
				if problem := g.packageDirProblem(ref); problem != "" {
					g.report(line, "%s %s", ref, problem)
				}
			}
			if m := runArgument.FindStringSubmatch(line.text); m != nil {
				if err := g.checkRun(line, m[1]+m[2]+m[3], refs); err != nil {
					return nil, err
				}
			}
			if m := conformanceVar.FindStringSubmatch(line.text); m != nil {
				value := pinnedLine{rel, line.line, m[2]}
				if m[1] == "CONFORMANCE_RUN" {
					conformanceRun = &value
				} else {
					conformancePkg = &value
				}
			}
		}
		// The single-bucket scenarios keep the pattern and the package in two
		// variables that deploy/ci/scenario.mk joins into one go test command.
		switch {
		case conformanceRun != nil && conformancePkg != nil:
			refs := packageRefs(conformancePkg.text)
			if err := g.checkRun(*conformanceRun, conformanceRun.text, refs); err != nil {
				return nil, err
			}
		case conformanceRun != nil:
			g.report(*conformanceRun, "CONFORMANCE_RUN is set without CONFORMANCE_PKG")
		case conformancePkg != nil:
			g.report(*conformancePkg, "CONFORMANCE_PKG is set without CONFORMANCE_RUN")
		}
	}
	return g.problems, nil
}

func (g *pinnedGuard) report(at pinnedLine, format string, args ...any) {
	location := fmt.Sprintf("%s:%d: ", at.file, at.line)
	g.problems = append(g.problems, location+fmt.Sprintf(format, args...))
}

// readPinnedLines joins continuation lines so a command split over several
// is checked as one, and drops the \r of a Windows checkout, which would
// otherwise hide a package argument at the end of a line.
func readPinnedLines(root, rel string) ([]pinnedLine, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	var lines []pinnedLine
	var pending *pinnedLine
	for i, raw := range strings.Split(string(data), "\n") {
		text, continued := strings.CutSuffix(strings.TrimSuffix(raw, "\r"), "\\")
		if pending == nil {
			pending = &pinnedLine{rel, i + 1, text}
		} else {
			pending.text += " " + text
		}
		if !continued {
			lines = append(lines, *pending)
			pending = nil
		}
	}
	if pending != nil {
		lines = append(lines, *pending)
	}
	return lines, nil
}

func packageRefs(text string) []packageRef {
	var refs []packageRef
	for _, argument := range []*regexp.Regexp{packageArgument, modulePackageArgument} {
		for _, m := range argument.FindAllStringSubmatchIndex(text, -1) {
			// More path after the match, as in ./test/oci/run.sh or
			// $(MODULE)/internal/buildinfo.version, names a file or a symbol,
			// not a package.
			if m[1] < len(text) && !strings.ContainsRune(" \t'\",;)|&\\", rune(text[m[1]])) {
				continue
			}
			refs = append(refs, packageRef{strings.TrimPrefix(text[m[2]:m[3]], "./"), m[4] >= 0})
		}
	}
	return refs
}

func (r packageRef) String() string {
	if r.recursive {
		return "./" + r.dir + "/..."
	}
	return "./" + r.dir
}

func (g *pinnedGuard) packageDirProblem(ref packageRef) string {
	dir := filepath.Join(g.root, filepath.FromSlash(ref.dir))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "is not a directory"
	}
	if !ref.recursive {
		if sources, _ := filepath.Glob(filepath.Join(dir, "*.go")); len(sources) == 0 {
			return "holds no .go files"
		}
	}
	return ""
}

// checkRun requires every alternative of a -run pattern to select a
// top-level test in the command's packages, so a renamed test cannot hide
// behind another alternative that still matches.
func (g *pinnedGuard) checkRun(line pinnedLine, pattern string, refs []packageRef) error {
	pattern = strings.ReplaceAll(pattern, "$$", "$")
	if runVariable.MatchString(pattern) {
		return nil
	}
	var candidates, packages []string
	for _, ref := range refs {
		// A missing package is reported on its own.
		if g.packageDirProblem(ref) != "" {
			continue
		}
		names, err := g.testNames(ref)
		if err != nil {
			return err
		}
		candidates = append(candidates, names...)
		packages = append(packages, ref.String())
	}
	// Packages given only through a variable cannot be read here.
	if len(packages) == 0 {
		return nil
	}
	for _, alternative := range runAlternatives(pattern) {
		selects, err := regexp.Compile(alternative)
		if err != nil {
			g.report(line, "-run %q: %v", pattern, err)
		} else if !slices.ContainsFunc(candidates, selects.MatchString) {
			g.report(line, "-run %q: %q selects no Test or Fuzz function in %s",
				pattern, alternative, strings.Join(packages, " "))
		}
	}
	return nil
}

// testNames parses sources rather than asking go list, so files behind any
// build tag (the e2e target's -tags=e2e) count.
func (g *pinnedGuard) testNames(ref packageRef) ([]string, error) {
	if names, ok := g.names[ref]; ok {
		return names, nil
	}
	dir := filepath.Join(g.root, filepath.FromSlash(ref.dir))
	var names []string
	collect := func(_ string, decls []*ast.FuncDecl) error {
		for _, fn := range decls {
			if testKindName(fn.Name.Name, "Test") || testKindName(fn.Name.Name, "Fuzz") {
				names = append(names, fn.Name.Name)
			}
		}
		return nil
	}
	if ref.recursive {
		if err := walkTestSources(dir, collect); err != nil {
			return nil, err
		}
	} else {
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			decls, err := topLevelFuncs(file)
			if err != nil {
				return nil, err
			}
			if err := collect(file, decls); err != nil {
				return nil, err
			}
		}
	}
	g.names[ref] = names
	return names, nil
}

// runAlternatives splits a -run pattern as the testing package does: at each
// unbracketed '|', keeping only the top-level part before an unbracketed '/'.
func runAlternatives(pattern string) []string {
	var alternatives []string
	brackets, parens, start, slash := 0, 0, 0, -1
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '[':
			brackets++
		case ']':
			brackets = max(brackets-1, 0)
		case '(':
			if brackets == 0 {
				parens++
			}
		case ')':
			if brackets == 0 {
				parens--
			}
		case '/':
			if brackets == 0 && parens == 0 && slash < 0 {
				slash = i
			}
		case '|':
			if brackets == 0 && parens == 0 {
				alternatives = append(alternatives, topLevel(pattern, start, i, slash))
				start, slash = i+1, -1
			}
		}
	}
	return append(alternatives, topLevel(pattern, start, len(pattern), slash))
}

func topLevel(pattern string, start, end, slash int) string {
	if slash >= 0 {
		end = slash
	}
	return pattern[start:end]
}

// walkTestSources visits the top-level functions of every _test.go file
// below dir, skipping the testdata and dot-directories that go test ./...
// never enters.
func walkTestSources(dir string, visit func(file string, decls []*ast.FuncDecl) error) error {
	return filepath.WalkDir(dir, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if file != dir && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(file, "_test.go") {
			return nil
		}
		decls, err := topLevelFuncs(file)
		if err != nil {
			return err
		}
		return visit(file, decls)
	})
}

// topLevelFuncs parses the file rather than searching its text, so test
// sources quoted inside fixtures do not count.
func topLevelFuncs(file string) ([]*ast.FuncDecl, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var funcs []*ast.FuncDecl
	for _, decl := range parsed.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			funcs = append(funcs, fn)
		}
	}
	return funcs, nil
}

func isFuzzTarget(fn *ast.FuncDecl) bool {
	if !testKindName(fn.Name.Name, "Fuzz") || fn.Type.Params.NumFields() != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && selector.Sel.Name == "F"
}
