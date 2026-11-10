package layout

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/doc/comment"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// docHeadings is the order of the headings in a doc.go. Testing comes last,
// where the first doc.go files of the migration put it. An optional heading
// may be left out; no other heading is allowed.
var docHeadings = []string{
	"Role",
	"Inputs",
	"Outputs",
	"Invariants",
	"References",
	"Status",
	"Testing",
}

// requiredHeadings are the headings every doc.go has.
var requiredHeadings = []string{"Role", "Inputs", "Outputs", "References", "Status"}

// placeholderStatus is the Status of a package whose only Go file is doc.go.
// Matching it is what makes the change that adds code to a placeholder also
// rewrite its Status.
var placeholderStatus = regexp.MustCompile(
	`^Placeholder: implemented by P\d{1,2}[a-z]?(, P\d{1,2}[a-z]?)*\.`,
)

// layerWord matches the layer that the Role of a package under internal/ names.
var layerWord = regexp.MustCompile(
	`(?i)\b(api|core|adapters?|ecosystem|platform|kernel|app|devtools|gateway|layout|test support|module root)\b`,
)

func TestUnitPackageDocs(t *testing.T) {
	for _, p := range checkPackageDocs(repopath.Root(t)) {
		t.Error(p)
	}
}

// checkPackageDocs returns, sorted, every way a package directory under root
// falls short of the documentation standard.
func checkPackageDocs(root string) []string {
	dirs, err := repopath.GoDirs(root)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, dir := range dirs {
		problems = append(problems, checkPackageDoc(root, dir)...)
	}
	slices.Sort(problems)
	return problems
}

// checkPackageDoc checks the package in dir, a root-relative directory: its
// doc.go, and that no other file carries a package comment.
func checkPackageDoc(root, dir string) []string {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return []string{err.Error()}
	}
	var (
		problems  []string
		doc       *ast.File
		hasDoc    bool
		goFiles   int
		names     = map[string]bool{} // packages of the non-test files besides doc.go
		testNames = map[string]bool{} // packages of the test files, less "_test"
	)
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		goFiles++
		hasDoc = hasDoc || name == "doc.go"
		rel := path.Join(dir, name)
		f, err := parseGoFile(fset, root, rel, name == "doc.go")
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		switch {
		case name == "doc.go":
			doc = f
		case strings.HasSuffix(name, "_test.go"):
			testNames[strings.TrimSuffix(f.Name.Name, "_test")] = true
		default:
			names[f.Name.Name] = true
		}
		// oapi-codegen writes a package comment into every client it
		// generates, and make generate would restore a deleted one.
		if name != "doc.go" && !strings.HasSuffix(name, ".gen.go") && f.Doc != nil {
			problems = append(problems, rel+": has a package comment; only doc.go may")
		}
	}
	switch {
	case !hasDoc:
		return append(problems, dir+": has no doc.go")
	case doc == nil: // doc.go did not parse, which is already reported
		return problems
	}
	return append(problems, checkDocFile(dir, doc, goFiles == 1, names, testNames)...)
}

// parseGoFile parses the file at rel, named by its root-relative path so that
// a message reads the same wherever the tree is. Only doc.go is parsed past
// the package clause.
func parseGoFile(fset *token.FileSet, root, rel string, full bool) (*ast.File, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	mode := parser.ParseComments
	if !full {
		mode |= parser.PackageClauseOnly
	}
	return parser.ParseFile(fset, rel, src, mode)
}

// checkDocFile checks f, the doc.go of the package in dir. placeholder
// reports that doc.go is the only Go file in dir; names and testNames are
// the packages its other files declare.
func checkDocFile(
	dir string,
	f *ast.File,
	placeholder bool,
	names, testNames map[string]bool,
) []string {
	file := path.Join(dir, "doc.go")
	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, file+": "+fmt.Sprintf(format, args...))
	}

	if len(f.Imports) > 0 {
		report("imports packages; doc.go holds only the package comment and clause")
	}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); !ok || g.Tok != token.IMPORT {
			report("declares identifiers; doc.go holds only the package comment and clause")
			break
		}
	}
	for _, g := range f.Comments {
		for _, c := range g.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				report("has a build constraint, which drops the package comment from other builds")
			}
		}
	}

	pkg := f.Name.Name
	switch {
	case len(names) > 0 && !names[pkg]:
		report(
			"declares package %s; the directory's package is %s",
			pkg,
			strings.Join(slices.Sorted(maps.Keys(names)), ", "),
		)
	case len(names) == 0 && len(testNames) > 0 && !testNames[pkg]:
		report(
			"declares package %s; the directory's tests are package %s",
			pkg, strings.Join(slices.Sorted(maps.Keys(testNames)), ", "),
		)
	}

	if f.Doc == nil {
		report("has no package comment")
		return problems
	}
	text := f.Doc.Text()
	first := "Package " + pkg + " "
	if pkg == "main" {
		first = "Command " + path.Base(dir) + " "
	}
	if !strings.HasPrefix(text, first) {
		report("the package comment must start %q", first)
	}
	for _, p := range checkSections(dir, text, placeholder) {
		report("%s", p)
	}
	return problems
}

// docSection is a heading of a package comment and the blocks under it.
type docSection struct {
	name   string
	blocks []comment.Block
}

// checkSections checks the headings of text, a package comment of a package
// in dir, and the text under Status and, for packages under internal/, Role.
func checkSections(dir, text string, placeholder bool) []string {
	// go/doc/comment also turns a lone capitalised line between paragraphs
	// into a heading. Counting the "# " lines tells such an old-style heading
	// apart from one written on purpose.
	raw := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		if h, ok := strings.CutPrefix(line, "# "); ok {
			raw[h]++
		}
	}
	var (
		sections []*docSection
		p        comment.Parser
	)
	for _, b := range p.Parse(text).Content {
		if h, ok := b.(*comment.Heading); ok {
			sections = append(sections, &docSection{name: plainText(h.Text)})
		} else if len(sections) > 0 {
			s := sections[len(sections)-1]
			s.blocks = append(s.blocks, b)
		}
	}

	var problems []string
	seen := map[string]bool{}
	prev := -1
	for _, s := range sections {
		if raw[s.name] < 1 {
			problems = append(
				problems,
				fmt.Sprintf("%q is an old-style heading; write it as \"# %s\"", s.name, s.name),
			)
		}
		raw[s.name]--
		i := slices.Index(docHeadings, s.name)
		switch {
		case i < 0:
			problems = append(
				problems,
				fmt.Sprintf("heading %q is not one of %s", s.name, strings.Join(docHeadings, ", ")),
			)
		case seen[s.name]:
			problems = append(problems, fmt.Sprintf("heading %q appears more than once", s.name))
		case i < prev:
			problems = append(
				problems,
				fmt.Sprintf(
					"heading %q is out of order; the order is %s",
					s.name,
					strings.Join(docHeadings, ", "),
				),
			)
		}
		if i >= 0 {
			seen[s.name] = true
			prev = i
		}
		if len(s.blocks) == 0 {
			problems = append(problems, fmt.Sprintf("heading %q has no text under it", s.name))
		}
	}
	for _, h := range requiredHeadings {
		if !seen[h] {
			problems = append(problems, fmt.Sprintf("has no %q heading", h))
		}
	}

	status := sectionText(sections, "Status")
	switch {
	case !seen["Status"]:
	case placeholder && !placeholderStatus.MatchString(status):
		problems = append(problems, fmt.Sprintf(
			"doc.go is the directory's only Go file, so Status must match %q; it reads %q",
			placeholderStatus, status,
		))
	case !placeholder && strings.HasPrefix(status, "Placeholder"):
		problems = append(problems, fmt.Sprintf(
			"Status reads %q, but the directory holds code; give the implemented phases", status,
		))
	}
	if strings.HasPrefix(dir+"/", root+"/") && seen["Role"] &&
		!layerWord.MatchString(sectionText(sections, "Role")) {
		problems = append(
			problems,
			"Role names no layer; it must contain one of the words in "+layerWord.String(),
		)
	}
	return problems
}

// sectionText returns the text under the first heading called name, with
// runs of white space reduced to one space.
func sectionText(sections []*docSection, name string) string {
	for _, s := range sections {
		if s.name == name {
			var parts []string
			for _, b := range s.blocks {
				parts = append(parts, blockText(b))
			}
			return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
		}
	}
	return ""
}

// blockText returns the text of a block below a heading.
func blockText(b comment.Block) string {
	switch b := b.(type) {
	case *comment.Paragraph:
		return plainText(b.Text)
	case *comment.List:
		var parts []string
		for _, item := range b.Items {
			for _, c := range item.Content {
				parts = append(parts, blockText(c))
			}
		}
		return strings.Join(parts, " ")
	case *comment.Code:
		return b.Text
	}
	return ""
}

// plainText returns the text of a run of doc comment text, without markup.
func plainText(text []comment.Text) string {
	var b strings.Builder
	for _, t := range text {
		switch t := t.(type) {
		case comment.Plain:
			b.WriteString(string(t))
		case comment.Italic:
			b.WriteString(string(t))
		case *comment.Link:
			b.WriteString(plainText(t.Text))
		case *comment.DocLink:
			b.WriteString(plainText(t.Text))
		}
	}
	return b.String()
}

// fixtureDoc returns a doc.go that meets the standard, for package pkg with
// the given first words, Role and Status.
func fixtureDoc(first, pkg, role, status string) string {
	return "// " + first + ` checks the fixtures.
//
// # Role
//
// ` + role + `
//
// # Inputs
//
// None.
//
// # Outputs
//
// None.
//
// # References
//
// Decision 0044.
//
// # Status
//
// ` + status + `
package ` + pkg + "\n"
}

func TestUnitPackageDocsRejectsNonConformingPackages(t *testing.T) {
	const (
		libDoc   = "internal/devtools/lib/doc.go"
		laterDoc = "internal/platform/later/doc.go"
		cmdDoc   = "tools/cmd/doc.go"
		suiteDoc = "test/server/suite/doc.go"
	)
	// A conforming tree: a package with code and tests, a placeholder, a
	// command, a test-only directory and a generated client, the last two
	// outside internal/, where Role needs no layer word.
	conforming := map[string]string{
		libDoc: fixtureDoc(
			"Package lib",
			"lib",
			"devtools layer.",
			"Implemented P9d.",
		),
		"internal/devtools/lib/lib.go":      "package lib\n",
		"internal/devtools/lib/lib_test.go": "package lib_test\n",
		laterDoc: fixtureDoc(
			"Package later",
			"later",
			"platform module, core layer.",
			"Placeholder: implemented by P11, P16b.",
		),
		cmdDoc: fixtureDoc(
			"Command cmd",
			"main",
			"command.",
			"Implemented P0.",
		),
		"tools/cmd/main.go": "package main\n",
		suiteDoc: fixtureDoc(
			"Package suite",
			"suite",
			"server-assembly suite.",
			"Implemented P8.",
		),
		"test/server/suite/suite_test.go": "package suite_test\n",
		"pkg/client/gen/doc.go": fixtureDoc(
			"Package gen",
			"gen",
			"generated client.",
			"Implemented P1b.",
		),
		"pkg/client/gen/client.gen.go": "// Package gen provides primitives.\npackage gen\n",
	}

	// set writes content to file, or deletes file when content is empty.
	set := func(file, content string) func(*testing.T, map[string]string) {
		return func(_ *testing.T, files map[string]string) {
			if content == "" {
				delete(files, file)
				return
			}
			files[file] = content
		}
	}
	// replace replaces old with replacement in file, which must hold old.
	replace := func(file, old, replacement string) func(*testing.T, map[string]string) {
		return func(t *testing.T, files map[string]string) {
			t.Helper()
			if !strings.Contains(files[file], old) {
				t.Fatalf("%s does not hold %q", file, old)
			}
			files[file] = strings.Replace(files[file], old, replacement, 1)
		}
	}
	// both applies two changes.
	both := func(a, b func(*testing.T, map[string]string)) func(*testing.T, map[string]string) {
		return func(t *testing.T, files map[string]string) {
			t.Helper()
			a(t, files)
			b(t, files)
		}
	}
	const statusEnd = "// Implemented P9d.\npackage lib"

	for _, tc := range []struct {
		name   string
		change func(*testing.T, map[string]string)
		want   string // the one problem expected, or empty for none
	}{
		{name: "conforming"},
		{name: "no doc.go", change: set(libDoc, ""), want: "internal/devtools/lib: has no doc.go"},
		{
			name: "imports", change: replace(libDoc, "package lib\n", "package lib\n\nimport _ \"embed\"\n"),
			want: libDoc + ": imports packages",
		},
		{
			name: "declarations", change: replace(libDoc, "package lib\n", "package lib\n\nconst x = 1\n"),
			want: libDoc + ": declares identifiers",
		},
		{
			name: "go:build line", change: replace(libDoc, "// Package lib", "//go:build linux\n\n// Package lib"),
			want: libDoc + ": has a build constraint",
		},
		{
			name: "+build line", change: replace(libDoc, "// Package lib", "// +build linux\n\n// Package lib"),
			want: libDoc + ": has a build constraint",
		},
		{name: "no package comment", change: set(libDoc, "package lib\n"), want: libDoc + ": has no package comment"},
		{
			name:   "package name differs from the code's",
			change: set(libDoc, fixtureDoc("Package other", "other", "devtools layer.", "Implemented P9d.")),
			want:   libDoc + ": declares package other; the directory's package is lib",
		},
		{
			name:   "package name differs from the tests'",
			change: set(suiteDoc, fixtureDoc("Package other", "other", "server-assembly suite.", "Implemented P8.")),
			want:   suiteDoc + ": declares package other; the directory's tests are package suite",
		},
		{
			name:   "package comment in another file",
			change: set("internal/devtools/lib/lib.go", "// Package lib again.\npackage lib\n"),
			want:   "internal/devtools/lib/lib.go: has a package comment; only doc.go may",
		},
		{
			name:   "package comment in a test file",
			change: set("internal/devtools/lib/lib_test.go", "// Package lib_test tests.\npackage lib_test\n"),
			want:   "internal/devtools/lib/lib_test.go: has a package comment; only doc.go may",
		},
		{
			name: "first sentence", change: replace(libDoc, "// Package lib checks", "// Lib checks"),
			want: libDoc + `: the package comment must start "Package lib "`,
		},
		{
			name: "first sentence of a command", change: replace(cmdDoc, "// Command cmd", "// Package main"),
			want: cmdDoc + `: the package comment must start "Command cmd "`,
		},
		{
			name: "old-style heading", change: replace(libDoc, "// # Inputs", "// Inputs"),
			want: libDoc + `: "Inputs" is an old-style heading`,
		},
		{
			name:   "heading out of order",
			change: replace(libDoc, statusEnd, "// Implemented P9d.\n//\n// # Invariants\n//\n// None.\npackage lib"),
			want:   libDoc + `: heading "Invariants" is out of order`,
		},
		{
			name:   "heading twice",
			change: replace(libDoc, statusEnd, "// Implemented P9d.\n//\n// # Status\n//\n// Again.\npackage lib"),
			want:   libDoc + `: heading "Status" appears more than once`,
		},
		{
			name:   "unknown heading",
			change: replace(libDoc, statusEnd, "// Implemented P9d.\n//\n// # Usage\n//\n// Run it.\npackage lib"),
			want:   libDoc + `: heading "Usage" is not one of`,
		},
		{
			name: "heading without text", change: replace(libDoc, "// # Inputs\n//\n// None.\n//\n", "// # Inputs\n//\n"),
			want: libDoc + `: heading "Inputs" has no text under it`,
		},
		{
			name: "missing heading", change: replace(libDoc, "// # References\n//\n// Decision 0044.\n//\n", ""),
			want: libDoc + `: has no "References" heading`,
		},
		{
			name: "placeholder without the placeholder status",
			change: replace(
				laterDoc, "Placeholder: implemented by P11, P16b.", "Placeholder: implemented in P11.",
			),
			want: laterDoc + ": doc.go is the directory's only Go file, so Status must match",
		},
		{
			name:   "placeholder status on a package with code",
			change: set("internal/platform/later/later.go", "package later\n"),
			want:   laterDoc + `: Status reads "Placeholder: implemented by P11, P16b.", but the directory holds code`,
		},
		{
			name: "role without a layer", change: replace(libDoc, "devtools layer.", "a library."),
			want: libDoc + ": Role names no layer",
		},
		{
			name:   "unparsable file",
			change: set("internal/devtools/lib/lib.go", "packag lib\n"),
			want:   "internal/devtools/lib/lib.go:1:1: expected 'package'",
		},
		{
			name:   "unparsable doc.go",
			change: set(libDoc, "// Package lib checks the fixtures.\npackag lib\n"),
			want:   libDoc + ":2:1: expected 'package'",
		},
		{
			name: "worktree copies are skipped",
			change: both(
				set(".worktrees/copy/go.mod", "module example.com/copy\n"),
				set(".worktrees/copy/internal/devtools/x/x.go", "package x\n"),
			),
		},
		{
			name:   "nested modules are skipped",
			change: both(set("nested/go.mod", "module example.com/nested\n"), set("nested/x/x.go", "package x\n")),
		},
		{name: "testdata is skipped", change: set("internal/devtools/lib/testdata/x/x.go", "package x\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := maps.Clone(conforming)
			if tc.change != nil {
				tc.change(t, files)
			}
			root := t.TempDir()
			for f, content := range files {
				write(t, filepath.Join(root, filepath.FromSlash(f)), content)
			}
			problems := checkPackageDocs(root)
			if tc.want == "" && problems != nil {
				t.Fatalf("problems in a conforming tree: %q", problems)
			}
			if tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.want)) {
				t.Fatalf("problems = %q; want one containing %q", problems, tc.want)
			}
		})
	}

	t.Run("problems come sorted", func(t *testing.T) {
		// lib.go is read before doc.go is checked, so its problem is found
		// first and sorts second.
		files := maps.Clone(conforming)
		replace(libDoc, "package lib\n", "package lib\n\nimport _ \"embed\"\n")(t, files)
		set("internal/devtools/lib/lib.go", "// Package lib again.\npackage lib\n")(t, files)
		root := t.TempDir()
		for f, content := range files {
			write(t, filepath.Join(root, filepath.FromSlash(f)), content)
		}
		want := []string{
			libDoc + ": imports packages; doc.go holds only the package comment and clause",
			"internal/devtools/lib/lib.go: has a package comment; only doc.go may",
		}
		if got := checkPackageDocs(root); !slices.Equal(got, want) {
			t.Fatalf("problems = %q; want %q", got, want)
		}
	})
}
