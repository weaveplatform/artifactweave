package pkgmove

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
)

// fileEntry is a file, or an asset directory, that a ticket's file entry
// moves.
type fileEntry struct {
	// from and to are root-relative; an asset directory ends in "/" in both.
	from, to string
	ticket   string
	// file is the ledger entry, for messages that ask for another entry.
	file ledger.File
}

// selectFiles returns the pending file move entries of ticket, and a notice
// for each of its split and delete file entries, which are carried out by
// hand.
func (t *tree) selectFiles(ticket string) ([]*fileEntry, []string, error) {
	var (
		files   []*fileEntry
		notices []string
	)
	for _, f := range t.report.Files {
		switch {
		case f.Ticket != ticket:
		case f.Action != ledger.ActionMove:
			notices = append(notices, fmt.Sprintf(
				"%s is a %s file entry; move carries only file move entries", f.From, f.Action))
		case f.Status == ledger.StatusDone:
			return nil, nil, fmt.Errorf("%w: %s is done, not pending", errPrecondition, f.From)
		default:
			// In a clean tree a pending entry's old path is on disk, so a
			// broken entry is one whose old path is gone or has another shape,
			// which requireShape names.
			if err := t.requireShape(f.From); err != nil {
				return nil, nil, err
			}
			files = append(files, &fileEntry{
				from:   f.From,
				to:     t.ledger.FileTarget(f.File),
				ticket: ticket,
				file:   f.File,
			})
		}
	}
	return files, notices, nil
}

// requireShape checks that the from of a file entry says what the tree holds:
// a tracked file, or with a trailing "/" an asset directory that holds
// tracked files. The derived status reads a mismatch as a missing path, so
// the mismatch is named before the status is.
func (t *tree) requireShape(from string) error {
	dir, asset := strings.CutSuffix(from, "/")
	switch {
	case asset && t.tracked[dir]:
		return fmt.Errorf(
			"%w: %s is a file; write it without the trailing / in the ledger",
			errPrecondition,
			from,
		)
	case asset && len(t.under(dir)) == 0:
		return fmt.Errorf("%w: %s holds no tracked file", errPrecondition, from)
	case !asset && len(t.under(from)) > 0:
		return fmt.Errorf(
			"%w: %s is a directory; write it as %s/ in the ledger", errPrecondition, from, from)
	case !asset && !t.tracked[from]:
		return fmt.Errorf("%w: %s is not a tracked file", errPrecondition, from)
	}
	return nil
}

// fileCarry is what a ticket's file entries carry.
type fileCarry struct {
	moves []fileMove
	// clauses holds the package clause change of each moved Go file, by its
	// old path.
	clauses map[string]*clauseRename
	// tests are the moved test files; fuzz are those that declare a fuzz
	// target.
	tests, fuzz []fileMove
	notices     []string
	// names caches the package name of each directory.
	names map[string]string
}

// carryFiles returns what files carry: each file, or each tracked file of an
// asset directory, at its target, and for each moved Go file its clause. It
// refuses a target that another move or a tracked file already takes and a
// fuzz target whose seed corpus would stay behind. moves are the files the
// package entries carry, which entries are.
func (t *tree) carryFiles(
	entries []*entry,
	files []*fileEntry,
	moves []fileMove,
) (*fileCarry, error) {
	fc := &fileCarry{clauses: map[string]*clauseRename{}, names: map[string]string{}}
	dest := map[string]string{}
	for _, m := range moves {
		dest[m.to] = m.from
	}
	byFrom := map[string]*fileEntry{}
	for _, f := range files {
		byFrom[f.from] = f
	}
	for _, f := range files {
		dir, asset := strings.CutSuffix(f.from, "/")
		sources := []string{f.from}
		if asset {
			sources = t.under(dir)
		}
		for _, src := range sources {
			to := strings.TrimSuffix(f.to, "/") + strings.TrimPrefix(src, dir)
			if other, taken := dest[to]; taken {
				return nil, fmt.Errorf(
					"%w: %s and %s would both move to %s; give one a file name in the ledger",
					errPrecondition, other, src, to,
				)
			}
			if t.tracked[to] {
				return nil, fmt.Errorf(
					"%w: %s would overwrite the tracked file %s", errPrecondition, src, to)
			}
			dest[to] = src
			fc.moves = append(fc.moves, fileMove{from: src, to: to})
			if strings.HasSuffix(src, ".go") && inPackage(src) && inPackage(to) {
				if err := t.carryGoFile(
					fc,
					f,
					fileMove{from: src, to: to},
					entries,
					byFrom,
				); err != nil {
					return nil, err
				}
			}
		}
	}
	return fc, nil
}

// carryGoFile records how the Go file m moves: its clause, whether it becomes
// an external test or carries a package comment into a staged package, and,
// for a test, the ownership and fuzz work. A fuzz target's seed corpus under
// testdata/fuzz must move with it, by a file entry of the same ticket.
func (t *tree) carryGoFile(
	fc *fileCarry,
	f *fileEntry,
	m fileMove,
	entries []*entry,
	byFrom map[string]*fileEntry,
) error {
	src, err := t.read(m.from)
	if err != nil {
		return err
	}
	file, err := parser.ParseFile(
		token.NewFileSet(), m.from, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("read the package clause: %w", err)
	}
	oldDir, newDir := path.Dir(m.from), path.Dir(m.to)
	oldName, err := t.dirName(fc, oldDir)
	if err != nil {
		return err
	}
	newName, err := t.targetName(fc, newDir, entries)
	if err != nil {
		return err
	}
	test := strings.HasSuffix(m.from, "_test.go")
	external := false
	if test && file.Name.Name == oldName {
		if external, err = t.externalOnly(newDir); err != nil {
			return err
		}
	}
	fc.clauses[m.from] = &clauseRename{oldName: oldName, newName: newName, external: external}
	if external {
		fc.notices = append(fc.notices, fmt.Sprintf(
			"%s is now an external test (package %s_test), as every test in %s is",
			m.to, newName, newDir))
	}
	if file.Doc != nil && path.Base(m.from) != "doc.go" && !ledger.IsLegacy(newDir) {
		fc.notices = append(fc.notices, fmt.Sprintf(
			"%s carries a package comment, but in %s only doc.go may (decision 0044)",
			m.to,
			newDir,
		))
	}
	if !test {
		return nil
	}
	fc.tests = append(fc.tests, m)
	targets := fuzzTargets(file)
	if len(targets) > 0 {
		fc.fuzz = append(fc.fuzz, m)
	}
	for _, name := range targets {
		corpus := oldDir + "/testdata/fuzz/" + name
		want := newDir + "/testdata/fuzz/" + name + "/"
		if c := byFrom[corpus+"/"]; len(t.under(corpus)) == 0 || (c != nil && c.to == want) {
			continue
		}
		to := fmt.Sprintf("%q", path.Join(f.file.To, "testdata/fuzz"))
		if f.file.Scope != "" {
			to += fmt.Sprintf(" and scope %q", f.file.Scope)
		}
		return fmt.Errorf(
			"%w: %s declares %s, so its seed corpus %s/ must move with it: "+
				"give it a file entry of %s with to %s",
			errPrecondition, m.from, name, corpus, f.ticket, to,
		)
	}
	return nil
}

// dirName returns the package that the tracked Go files directly in dir
// declare.
func (t *tree) dirName(fc *fileCarry, dir string) (string, error) {
	if name, ok := fc.names[dir]; ok {
		return name, nil
	}
	name, err := t.packageName(t.goFilesIn(dir))
	fc.names[dir] = name
	return name, err
}

// goFilesIn returns the tracked Go files directly in dir.
func (t *tree) goFilesIn(dir string) []string {
	var files []string
	for _, f := range t.under(dir) {
		if path.Dir(f) == dir && strings.HasSuffix(f, ".go") {
			files = append(files, f)
		}
	}
	return files
}

// targetName returns the package name a file takes in dir: that of the Go
// files dir holds, else the name of the package entry that targets dir, else
// the last element of dir.
func (t *tree) targetName(fc *fileCarry, dir string, entries []*entry) (string, error) {
	if len(t.goFilesIn(dir)) > 0 {
		return t.dirName(fc, dir)
	}
	for _, e := range entries {
		if e.to == dir {
			return e.newName, nil
		}
	}
	for _, p := range t.ledger.Packages {
		if t.target(p) == dir && p.Name != "" {
			return p.Name, nil
		}
	}
	return path.Base(dir), nil
}

// externalOnly reports whether dir holds tests, all of them in external test
// packages, and no Go file but doc.go besides, as the server suites under
// test/server do: an in-package test that moves there becomes an external
// test like them.
func (t *tree) externalOnly(dir string) (bool, error) {
	tests := 0
	for _, f := range t.goFilesIn(dir) {
		if !strings.HasSuffix(f, "_test.go") {
			if path.Base(f) != "doc.go" {
				return false, nil
			}
			continue
		}
		src, err := t.read(f)
		if err != nil {
			return false, err
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, src, parser.PackageClauseOnly)
		if err != nil {
			return false, fmt.Errorf("read the package clause: %w", err)
		}
		if !strings.HasSuffix(file.Name.Name, "_test") {
			return false, nil
		}
		tests++
	}
	return tests > 0, nil
}

// fuzzTargets returns the names of the fuzz targets that file declares: the
// functions named Fuzz or FuzzX, with X not a lower-case letter, that take one
// *testing.F, as go test and tools/citestselection read them.
func fuzzTargets(file *ast.File) []string {
	var names []string
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && isFuzzTarget(fn) {
			names = append(names, fn.Name.Name)
		}
	}
	return names
}

func isFuzzTarget(fn *ast.FuncDecl) bool {
	rest, ok := strings.CutPrefix(fn.Name.Name, "Fuzz")
	if r, _ := utf8.DecodeRuneInString(rest); !ok || fn.Recv != nil || unicode.IsLower(r) ||
		fn.Type.Params.NumFields() != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "F"
}

// makefilePath is the Makefile whose FUZZ_PKGS lists the packages whose seed
// corpora make test runs.
const makefilePath = "Makefile"

// fuzzVar matches the FUZZ_PKGS assignment as tools/citestselection's
// coupling test reads it, with a carriage return kept apart.
var fuzzVar = regexp.MustCompile(`(?m)^(FUZZ_PKGS\s*[:?]?=)(.*?)(\r?)$`)

// followFuzzTargets moves the FUZZ_PKGS entry of each package whose fuzz
// targets c moves: the target directory joins the list, in the old
// directory's place once no fuzz target remains there. make test runs the
// seed corpora of the listed packages only, and the coupling test of
// tools/citestselection fails while the list and the targets disagree.
func (t *tree) followFuzzTargets(c *change, fuzz []fileMove) error {
	if len(fuzz) == 0 || !t.tracked[makefilePath] {
		return nil
	}
	i := slices.IndexFunc(c.writes, func(w fileWrite) bool { return w.path == makefilePath })
	w := fileWrite{path: makefilePath, to: makefilePath}
	if i >= 0 {
		w = c.writes[i]
	} else {
		src, err := t.read(makefilePath)
		if err != nil {
			return err
		}
		w.content = src
	}
	loc := fuzzVar.FindSubmatchIndex(w.content)
	if loc == nil {
		return nil
	}
	moved := map[string]bool{}
	for _, m := range c.moves {
		moved[m.from] = true
	}
	fields := strings.Fields(string(w.content[loc[4]:loc[5]]))
	changed := 0
	for _, m := range fuzz {
		oldPkg, newPkg := "./"+path.Dir(m.from), "./"+path.Dir(m.to)
		left, err := t.fuzzLeft(path.Dir(m.from), moved)
		if err != nil {
			return err
		}
		at := slices.Index(fields, oldPkg)
		switch {
		case !slices.Contains(fields, newPkg) && at >= 0 && !left:
			fields[at] = newPkg
		case !slices.Contains(fields, newPkg):
			fields = append(fields, newPkg)
		case at >= 0 && !left:
			fields = slices.Delete(fields, at, at+1)
		default:
			continue
		}
		changed++
	}
	if changed == 0 {
		// Every moved target's package is listed already.
		return nil
	}
	w.counts.fuzz += changed
	w.content = slices.Concat(
		w.content[:loc[4]],
		[]byte(" "+strings.Join(fields, " ")),
		w.content[loc[5]:],
	)
	if i >= 0 {
		c.writes[i] = w
		return nil
	}
	// The rewrite lists the files it changes by path; the Makefile joins
	// them in its place.
	i, _ = slices.BinarySearchFunc(c.writes, makefilePath, func(w fileWrite, p string) int {
		return strings.Compare(w.path, p)
	})
	c.writes = slices.Insert(c.writes, i, w)
	return nil
}

// fuzzLeft reports whether a test file directly in dir that the change does
// not move declares a fuzz target.
func (t *tree) fuzzLeft(dir string, moved map[string]bool) (bool, error) {
	for _, f := range t.goFilesIn(dir) {
		if !strings.HasSuffix(f, "_test.go") || moved[f] {
			continue
		}
		src, err := t.read(f)
		if err != nil {
			return false, err
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, src, parser.SkipObjectResolution)
		if err != nil {
			return false, fmt.Errorf("parse %s: %w", f, err)
		}
		if len(fuzzTargets(file)) > 0 {
			return true, nil
		}
	}
	return false, nil
}
