package pkgmove

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

// configFiles are the configuration files whose text is rewritten, besides
// the Go files and the living Markdown. They name package paths in import
// paths, -X flags, coverage patterns, lint exclusions, fixture attributes,
// generator configuration and test selection.
var configFiles = []string{
	".golangci.yml",
	".testcoverage.yml",
	".gitattributes",
	"Makefile",
	"Dockerfile",
	".goreleaser.yaml",
	".github/workflows/*.yml",
	"deploy/platform/terraform/*/*/Makefile",
	"api/oapi-*.yaml",
	"api/src/**/*.yaml",
	"api/*.openapi.yaml",
	"tools/depsync/catalog.json",
	"tools/depsync/*.py",
	"test/acceptance/*.sh",
	"test/acceptance/features/*.feature",
	"docs/implementation/package-format-inventory.json",
	"docs/client-recipes/src/*.json",
	"**/migrations/*.sql",
}

// historyFiles are the documents that keep the paths they were written with:
// research, decisions about the migration, evidence and the ledger itself
// (migration/README.md, "History and living documents"). Nothing rewrites
// them and the stale check does not read them.
var historyFiles = []string{
	"CHANGELOG.md",
	"docs/research/*.md",
	"docs/research/architecture-options/**",
	"docs/research/evidence/**",
	"docs/research/decisions/0044-*.md",
	"docs/testing/evidence/**",
	"docs/testing/phase*.md",
	"docs/implementation/repo-layout.md",
	"docs/implementation/p9d-path-map.json",
	"migration/README.md",
	"migration/ledger*.json",
	// LegacyRoots. Each pkg/ root is also a package with its own entry, and
	// it stays a legacy root after that package moves, until promotion.
	// Rewritten, the list would name the staged package as legacy, and the
	// ledger tests and promote's empty-roots check would fail.
	"migration/devtools/ledger/ledger.go",
}

// ownershipPath is the server test ownership inventory, which is rewritten
// as JSON rather than as text.
const ownershipPath = "tools/citestselection/test_ownership.json"

// fileKind says how a change rewrites a tracked file.
type fileKind int

const (
	kindNone fileKind = iota
	kindGo
	kindText
	kindOwnership
)

// kindOf returns how a change rewrites the tracked file at p. Generated Go
// is regenerated instead (decision 0002).
func kindOf(p string) fileKind {
	switch {
	case matchAny(historyFiles, p) || strings.HasSuffix(p, ".gen.go"):
		return kindNone
	case strings.HasSuffix(p, ".go"):
		return kindGo
	case p == ownershipPath:
		return kindOwnership
	case strings.HasSuffix(p, ".md") || matchAny(configFiles, p):
		return kindText
	}
	return kindNone
}

// matchAny reports whether one of the slash-separated globs matches p. A
// "**" element matches any number of path elements; any other element
// matches one, as in path.Match.
func matchAny(globs []string, p string) bool {
	return slices.ContainsFunc(globs, func(g string) bool {
		return globMatch(strings.Split(g, "/"), strings.Split(p, "/"))
	})
}

func globMatch(glob, elems []string) bool {
	switch {
	case len(glob) == 0:
		return len(elems) == 0
	case glob[0] == "**":
		for i := 0; i <= len(elems); i++ {
			if globMatch(glob[1:], elems[i:]) {
				return true
			}
		}
		return false
	case len(elems) == 0:
		return false
	}
	ok, err := path.Match(glob[0], elems[0])
	return err == nil && ok && globMatch(glob[1:], elems[1:])
}

// editCounts says what a rewrite changed in one file, for the plan.
type editCounts struct {
	paths, imports, qualifiers, aliases, clauses, duplicates int
	// names counts the import names given to a renamed package whose
	// directory is named otherwise.
	names int
	// fuzz counts the FUZZ_PKGS entries a moved fuzz target changes, and
	// unused the imports that fix removes once nothing uses them.
	fuzz, unused int
}

func (c editCounts) String() string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{
		{c.imports, "import"},
		{c.qualifiers, "qualifier"},
		{c.names, "import name"},
		{c.aliases, "import alias"},
		{c.clauses, "package clause"},
		{c.duplicates, "removed repeated import"},
		{c.unused, "removed unused import"},
		{c.paths, "path"},
		{c.fuzz, "fuzz package"},
	} {
		switch {
		case p.n == 1:
			parts = append(parts, "1 "+p.what)
		case p.n > 1:
			parts = append(parts, strconv.Itoa(p.n)+" "+p.what+"s")
		}
	}
	return strings.Join(parts, ", ")
}

// edit replaces src[start:end] with text.
type edit struct {
	start, end int
	text       string
}

type edits []edit

// match adds a replacement for each reference that mt finds in text, which
// starts at byte off of the file, and returns how many it replaced. A
// reference without a single replacement, to a split or deleted package
// above a moved one, stays for the stale check to report: replacing it would
// delete it.
func (es *edits) match(mt *matcher, off int, text string) int {
	n := 0
	for _, r := range mt.find(text) {
		if r.repl != "" {
			*es = append(*es, edit{start: off + r.start, end: off + r.end, text: r.repl})
			n++
		}
	}
	return n
}

// apply returns src with the edits made in order. An insertion sorts before
// a replacement at the same place, so an import name lands before the path.
func (es edits) apply(src []byte) []byte {
	slices.SortStableFunc(es, func(a, b edit) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end))
	})
	var b bytes.Buffer
	last := 0
	for _, e := range es {
		b.Write(src[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.Write(src[last:])
	return b.Bytes()
}

// rewriter rewrites the files of one change.
type rewriter struct {
	mt *matcher
	// renamed maps the import path of each moved package whose name
	// changes to its entry.
	renamed map[string]*entry
	// moved maps each Go file whose package clause changes, by its old path,
	// to the change: a file that a renamed package carries, or one that a
	// file entry moves.
	moved map[string]*clauseRename
	typed map[string]*typedFile
	// skip are the -skip globs: files exempt from text rewriting, whose
	// imports are still rewritten.
	skip []string
}

// rewrite returns the new content of the tracked file at p, or src itself
// when nothing changes, with what changed. A Go file that does not parse,
// such as a broken fixture, is rewritten as text and reported in note.
func (rw *rewriter) rewrite(p string, src []byte) (out []byte, n editCounts, note string) {
	skip := matchAny(rw.skip, p)
	if kindOf(p) == kindGo {
		out, n, err := rw.rewriteGo(p, src, skip)
		if err == nil || skip {
			return out, n, ""
		}
		note = fmt.Sprintf("%s does not parse, so its paths were rewritten as text: %v", p, err)
	}
	if skip {
		return src, n, note
	}
	var es edits
	n.paths = es.match(rw.mt, 0, string(src))
	return es.apply(src), n, note
}

// rewriteGo rewrites a Go file: paths in string literals and comments
// (unless skipText), import paths, the qualifiers or import names of renamed
// packages, and the package clause of a renamed package's files. The result
// goes through go/format, so the import blocks stay sorted.
func (rw *rewriter) rewriteGo(p string, src []byte, skipText bool) ([]byte, editCounts, error) {
	var n editCounts
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return src, n, fmt.Errorf("parse: %w", err)
	}
	file := fset.File(f.Pos())
	var es edits
	if !skipText {
		ast.Inspect(f, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.ImportSpec:
				return false
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					n.paths += es.match(rw.mt, file.Offset(x.Pos()), x.Value)
				}
			}
			return true
		})
		for _, g := range f.Comments {
			for _, c := range g.List {
				n.paths += es.match(rw.mt, file.Offset(c.Pos()), c.Text)
			}
		}
	}
	for _, spec := range f.Imports {
		off := file.Offset(spec.Path.Pos())
		n.imports += es.match(rw.mt, off, spec.Path.Value)
		old, _ := strconv.Unquote(spec.Path.Value)
		e := rw.renamed[old]
		if e == nil || spec.Name != nil {
			continue
		}
		if uses, ok := qualify(rw.typed[p], off, e.newName); ok {
			for _, u := range uses {
				es = append(es, edit{start: u, end: u + len(e.oldName), text: e.newName})
			}
			n.qualifiers += len(uses)
			// goimports, which make fmt runs after the move, takes an
			// unnamed import for a package named like its path's last
			// element. For a package whose directory is named otherwise,
			// such as <type>http in http/, it then adds a named copy of
			// the import for the renamed qualifiers and can keep the
			// unnamed one, which Go rejects as a redeclaration. Naming the
			// import as goimports would leaves it nothing to add.
			if path.Base(e.to) != e.newName {
				es = append(es, edit{start: off, end: off, text: e.newName + " "})
				n.names++
			}
		} else {
			es = append(es, edit{start: off, end: off, text: e.oldName + " "})
			n.aliases++
		}
	}
	if cr := rw.moved[p]; cr != nil {
		n.clauses = renameClause(&es, file, f, cr)
	}
	if len(es) == 0 {
		return src, n, nil
	}
	out := es.apply(src)
	if n.imports > 0 {
		out, n.duplicates = dropDuplicateImports(out)
	}
	// A fixture below testdata keeps its layout; it may be unformatted on
	// purpose.
	if inPackage(p) {
		if formatted, err := format.Source(out); err == nil {
			out = formatted
		}
	}
	return out, n, nil
}

// dropDuplicateImports removes each import that repeats an earlier one with
// the same name and path, which Go rejects, and returns how many it removed.
// A file that imported two packages merged into one has such a repeat once
// both import paths name the target.
func dropDuplicateImports(src []byte) ([]byte, int) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return src, 0
	}
	file := fset.File(f.Pos())
	seen := map[string]bool{}
	var es edits
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok {
			es = append(es, repeatedImports(file, src, g, seen)...)
		}
	}
	return es.apply(src), len(es)
}

// repeatedImports returns an edit that removes each import of g whose name
// and path seen already holds, and adds the others to seen. An import
// without parentheses goes with its declaration, and the removal takes the
// rest of its line.
func repeatedImports(file *token.File, src []byte, g *ast.GenDecl, seen map[string]bool) []edit {
	var es []edit
	for _, s := range g.Specs {
		spec, _ := s.(*ast.ImportSpec)
		key := spec.Path.Value
		if spec.Name != nil {
			key = spec.Name.Name + " " + key
		}
		if !seen[key] {
			seen[key] = true
			continue
		}
		var node ast.Node = spec
		if !g.Lparen.IsValid() {
			node = g
		}
		start := file.Offset(file.LineStart(file.Line(node.Pos())))
		end := file.Offset(node.End())
		// IndexByte is -1 at the end of the source, which then ends the
		// removal.
		end += bytes.IndexByte(src[end:], '\n') + 1
		es = append(es, edit{start: start, end: end})
	}
	return es
}

// clauseRename says how the package clause of a moved Go file changes: a
// clause naming oldName becomes newName and oldName_test becomes
// newName_test. With external, an in-package test becomes newName_test too,
// joining a directory whose tests are all external. Any other clause, such as
// an ignored generator's main, stays.
type clauseRename struct {
	oldName, newName string
	external         bool
}

// clause returns the clause that a file declaring name has after the move.
func (cr *clauseRename) clause(name string) string {
	switch {
	case name == cr.oldName && cr.external:
		return cr.newName + "_test"
	case name == cr.oldName:
		return cr.newName
	case name == cr.oldName+"_test":
		return cr.newName + "_test"
	}
	return name
}

// renameClause renames the package clause of a moved file as cr says, and the
// "Package <name>" opening of its package comment, and returns how many
// clauses it renamed.
func renameClause(es *edits, file *token.File, f *ast.File, cr *clauseRename) int {
	name := cr.clause(f.Name.Name)
	if name == f.Name.Name {
		return 0
	}
	off := file.Offset(f.Name.Pos())
	*es = append(*es, edit{start: off, end: off + len(f.Name.Name), text: name})
	if f.Doc != nil {
		c := f.Doc.List[0]
		opening := "// Package " + cr.oldName
		rest, ok := strings.CutPrefix(c.Text, opening)
		if ok && (rest == "" || rest[0] == ' ') {
			start := file.Offset(c.Pos()) + len("// Package ")
			*es = append(*es, edit{start: start, end: start + len(cr.oldName), text: cr.newName})
		}
	}
	return 1
}

// qualify returns the offsets of the qualifiers that refer to the import
// whose path literal starts at byte pathOff of the file tf, and whether every
// one of them can become name: no use would then resolve to another object.
// It reports false for a file no typed load compiled (tf is nil), which then
// keeps the old name as an explicit import name.
func qualify(tf *typedFile, pathOff int, name string) ([]int, bool) {
	if tf == nil {
		return nil, false
	}
	file := tf.fset.File(tf.file.Pos())
	var pkgName types.Object
	for _, spec := range tf.file.Imports {
		if file.Offset(spec.Path.Pos()) == pathOff {
			pkgName = tf.info.Implicits[spec]
		}
	}
	var uses []int
	ok := pkgName != nil
	ast.Inspect(tf.file, func(node ast.Node) bool {
		sel, isSel := node.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		id, isIdent := sel.X.(*ast.Ident)
		if !isIdent || pkgName == nil || tf.info.Uses[id] != pkgName {
			return true
		}
		if _, other := tf.pkg.Scope().
			Innermost(id.Pos()).
			LookupParent(name, id.Pos()); other != nil {
			ok = false
		}
		uses = append(uses, file.Offset(id.Pos()))
		return true
	})
	return uses, ok
}

// rewriteAll rewrites files, tracked paths that may name an old path of
// rw.mt, and returns the new contents of those that change, at the path
// relocate gives them after the change, with the notices of the rewrite.
// The ownership inventory is rewritten as JSON as own says.
func (t *tree) rewriteAll(
	rw *rewriter,
	files []string,
	relocate func(string) string,
	own ownershipChange,
) ([]fileWrite, []string, error) {
	files = slices.Compact(slices.Sorted(slices.Values(files)))
	var (
		writes  []fileWrite
		notices []string
	)
	for _, p := range files {
		kind := kindOf(p)
		if kind == kindNone || (kind == kindOwnership && matchAny(rw.skip, p)) {
			continue
		}
		src, err := t.read(p)
		if err != nil {
			return nil, nil, err
		}
		var (
			out  []byte
			n    editCounts
			note []string
		)
		if kind == kindOwnership {
			out, note, err = rewriteOwnership(src, own)
			if err != nil {
				return nil, nil, err
			}
		} else {
			var s string
			out, n, s = rw.rewrite(p, src)
			if s != "" {
				note = []string{s}
			}
		}
		notices = append(notices, note...)
		if !bytes.Equal(out, src) {
			writes = append(writes, fileWrite{path: p, to: relocate(p), content: out, counts: n})
		}
	}
	return writes, notices, nil
}

// apply makes the change: git mv for the files and directories that move,
// git rm for the files that go, the new contents at their new paths, git add
// for the files it creates, and the removal of the directories the moves
// emptied.
func (c *change) apply() error {
	t := c.t
	if err := t.gitMove(c.moves, c.dirMoves); err != nil {
		return err
	}
	if len(c.removes) > 0 {
		if _, err := t.git.run(append([]string{"rm", "-q", "--"}, c.removes...)...); err != nil {
			return err
		}
	}
	for _, w := range c.writes {
		if err := t.mkdir(path.Dir(w.to)); err != nil {
			return err
		}
		// An existing file keeps its permissions; WriteFile applies the
		// mode only to a file it creates.
		if err := os.WriteFile(t.abs(w.to), w.content, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", w.to, err)
		}
	}
	if len(c.adds) > 0 {
		if _, err := t.git.run(append([]string{"add", "--"}, c.adds...)...); err != nil {
			return err
		}
	}
	var emptied []string
	for _, m := range slices.Concat(c.moves, c.dirMoves) {
		for d := path.Dir(m.from); d != "."; d = path.Dir(d) {
			emptied = append(emptied, d)
		}
	}
	// Deepest first, so a parent is empty by the time it is tried; os.Remove
	// leaves a directory that still holds anything.
	slices.SortFunc(emptied, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	for _, d := range slices.Compact(emptied) {
		_ = os.Remove(t.abs(d))
	}
	return nil
}

// gitMoveBatch is how many sources one git mv takes, which keeps a command
// line well below the Windows limit of 32767 characters.
const gitMoveBatch = 100

// gitMove moves files and directories with git mv, after creating every
// target directory: files that keep their name in batches per target
// directory, a renamed file or a directory on its own. A directory moves to
// a path that does not exist yet, as git mv would otherwise put it inside,
// so only its parent is created.
func (t *tree) gitMove(moves, dirMoves []fileMove) error {
	kept := map[string][]string{}
	var single []fileMove
	for _, m := range moves {
		// Every target directory gets a key, so that it is created even
		// when each file moving into it is renamed.
		dir := path.Dir(m.to)
		names := kept[dir]
		if path.Base(m.from) == path.Base(m.to) {
			names = append(names, m.from)
		} else {
			single = append(single, m)
		}
		kept[dir] = names
	}
	for _, d := range dirMoves {
		if _, ok := kept[path.Dir(d.to)]; !ok {
			kept[path.Dir(d.to)] = nil
		}
		single = append(single, d)
	}
	var commands [][]string
	for _, dir := range slices.Sorted(maps.Keys(kept)) {
		if err := t.mkdir(dir); err != nil {
			return err
		}
		for batch := range slices.Chunk(kept[dir], gitMoveBatch) {
			commands = append(
				commands,
				slices.Concat([]string{"mv", "--"}, batch, []string{dir + "/"}),
			)
		}
	}
	for _, m := range single {
		commands = append(commands, []string{"mv", "--", m.from, m.to})
	}
	for _, args := range commands {
		if _, err := t.git.run(args...); err != nil {
			return err
		}
	}
	return nil
}

// mkdir creates the root-relative directory dir and its parents.
func (t *tree) mkdir(dir string) error {
	if err := os.MkdirAll(t.abs(dir), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}
