package pkgmove

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
)

// fix runs pkgmove fix.
func (e *env) fix(args []string) error {
	var (
		ticket                                     string
		bridgesFlag, external, reportOnly, noRegen bool
	)
	fs, err := parse("fix", args, func(fs *flag.FlagSet) {
		fs.StringVar(&ticket, "ticket", "", "repair what this ticket's file moves broke")
		fs.BoolVar(&bridgesFlag, "bridges", false, "point each use of a bridge at its target")
		fs.BoolVar(&external, "external", false, "also make the named tests external")
		fs.BoolVar(&reportOnly, "report-only", false, "print the plan and change nothing")
		fs.BoolVar(&noRegen, "no-regenerate", false, "skip make fmt after the rewrite")
	})
	if err != nil {
		return err
	}
	switch {
	case (ticket == "") == !bridgesFlag:
		return fmt.Errorf("%w: fix takes either -ticket or -bridges", errUsage)
	case external != (fs.NArg() > 0):
		return fmt.Errorf(
			"%w: -external takes the test files to convert, and only it takes arguments",
			errUsage,
		)
	case external && bridgesFlag:
		return fmt.Errorf("%w: -external goes with -ticket", errUsage)
	}
	t, err := openTree(e.ctx, e.dir)
	if err != nil {
		return err
	}
	if err := t.validate(); err != nil {
		return err
	}
	if err := t.scan(); err != nil {
		return err
	}
	var plan *fixPlan
	if bridgesFlag {
		plan, err = t.planBridges()
	} else {
		plan, err = t.planFix(ticket, fs.Args())
	}
	if err != nil {
		return err
	}
	return e.finishFix(t, plan, reportOnly, noRegen)
}

// finishFix prints the plan and, unless reportOnly, makes it: each rewritten
// file with what changed, then each problem as file:line: message. make fmt
// formats the rewritten files unless noRegen. Problems make the run exit 1,
// after the rewrite: they are what is left for hand work.
func (e *env) finishFix(t *tree, plan *fixPlan, reportOnly, noRegen bool) error {
	var writes []fileWrite
	for _, rel := range slices.Sorted(maps.Keys(plan.files)) {
		g := plan.files[rel]
		src, err := t.read(rel)
		if err != nil {
			return err
		}
		out, err := g.apply(src, t.module)
		if err != nil {
			return err
		}
		if !bytes.Equal(out, src) {
			writes = append(writes, fileWrite{path: rel, to: rel, content: out, counts: g.counts})
		}
	}
	for _, n := range plan.notes {
		fmt.Fprintf(e.stdout, "note: %s\n", n)
	}
	for _, w := range writes {
		fmt.Fprintf(e.stdout, "  rewrite %s: %s\n", w.path, w.counts)
	}
	problems := plan.sortedProblems()
	for _, p := range problems {
		fmt.Fprintln(e.stdout, p)
	}
	switch {
	case len(writes) == 0 && len(problems) == 0:
		fmt.Fprintln(e.stdout, "fix: nothing to rewrite")
		return nil
	case reportOnly:
		fmt.Fprintln(e.stdout, "report only: nothing changed")
	default:
		for _, w := range writes {
			// The file exists, so it keeps its permissions.
			if err := os.WriteFile(t.abs(w.path), w.content, 0o600); err != nil {
				return fmt.Errorf("write %s: %w", w.path, err)
			}
		}
		if len(writes) > 0 && !noRegen {
			fmtStep := step{[]string{"make", "fmt"}, "format the rewritten files"}
			if err := e.regenerate(t.root, []step{fmtStep}); err != nil {
				return err
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %d problems left for hand work", errProblems, len(problems))
	}
	return nil
}

// ticketMoves returns the Go files that the file move entries of ticket
// moved, by old and new path. fix repairs what move did, so a pending or
// broken entry is refused. A ticket without file move entries is refused
// too, unless external has files to convert.
func (t *tree) ticketMoves(ticket string, external bool) ([]fileMove, error) {
	var moves []fileMove
	entries := 0
	for _, f := range t.report.Files {
		if f.Ticket != ticket || f.Action != ledger.ActionMove {
			continue
		}
		entries++
		if f.Status != ledger.StatusDone {
			return nil, fmt.Errorf("%w: %s is %s; run pkgmove move -ticket %s first",
				errPrecondition, f.From, f.Status, ticket)
		}
		to := t.ledger.FileTarget(f.File)
		if strings.HasSuffix(f.From, ".go") && inPackage(f.From) && inPackage(to) {
			moves = append(moves, fileMove{from: f.From, to: to})
		}
	}
	if entries == 0 && !external {
		return nil, fmt.Errorf(
			"%w: %s has no file move entry in the ledger; fix repairs what file moves break",
			errPrecondition, ticket)
	}
	return moves, nil
}

// decl is one package-level declaration.
type decl struct {
	// file is the root-relative file that declares it, at line.
	file string
	line int
	// test marks a declaration in a _test.go file, external one in an
	// external test package, and bridge one in a bridge file.
	test, external, bridge bool
}

// movedDecl is a declaration that a moved file took to dir.
type movedDecl struct {
	dir string
	decl
}

// splitScope is what the file moves of a ticket did.
type splitScope struct {
	// from maps the new path of each moved Go file to its old path.
	from map[string]string
	// oldDirs and newDirs are the directories the files left and joined.
	oldDirs, newDirs []string
	// decls holds the package-level declarations of each old and new
	// directory as the tree has them now, and names its package name.
	decls map[string]map[string]decl
	names map[string]string
	// moved holds, for each old directory, the declarations that its moved
	// files took along, by name.
	moved map[string]map[string]movedDecl
}

// splitScope indexes the declarations that moves left behind and took along.
func (t *tree) splitScope(moves []fileMove) (*splitScope, error) {
	sc := &splitScope{
		from:  map[string]string{},
		decls: map[string]map[string]decl{},
		names: map[string]string{},
		moved: map[string]map[string]movedDecl{},
	}
	for _, m := range moves {
		sc.from[m.to] = m.from
		sc.oldDirs = append(sc.oldDirs, path.Dir(m.from))
		sc.newDirs = append(sc.newDirs, path.Dir(m.to))
	}
	slices.Sort(sc.oldDirs)
	slices.Sort(sc.newDirs)
	sc.oldDirs, sc.newDirs = slices.Compact(sc.oldDirs), slices.Compact(sc.newDirs)
	dirs := slices.Concat(sc.oldDirs, sc.newDirs)
	slices.Sort(dirs)
	for _, dir := range slices.Compact(dirs) {
		sc.decls[dir] = map[string]decl{}
		for _, f := range t.goFilesIn(dir) {
			if err := t.declare(f, sc.decls[dir], sc.names); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range moves {
		ds := map[string]decl{}
		if err := t.declare(m.to, ds, sc.names); err != nil {
			return nil, err
		}
		old := path.Dir(m.from)
		if sc.moved[old] == nil {
			sc.moved[old] = map[string]movedDecl{}
		}
		for name, d := range ds {
			sc.moved[old][name] = movedDecl{dir: path.Dir(m.to), decl: d}
		}
	}
	return sc, nil
}

// declare adds the package-level declarations of the Go file rel to into,
// where an earlier file's declaration of a name stays, and records the
// package name of a file that is no test in names.
func (t *tree) declare(rel string, into map[string]decl, names map[string]string) error {
	src, err := t.read(rel)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("%w: %s does not parse: %w", errPrecondition, rel, err)
	}
	d := decl{
		file:     rel,
		test:     strings.HasSuffix(rel, "_test.go"),
		external: strings.HasSuffix(f.Name.Name, "_test"),
		bridge:   ledger.IsBridge(path.Base(rel)),
	}
	if dir := path.Dir(rel); !d.test && names[dir] == "" {
		names[dir] = f.Name.Name
	}
	for _, dl := range f.Decls {
		for _, id := range declNames(dl) {
			if _, ok := into[id.Name]; !ok && id.Name != "_" && id.Name != "init" {
				d.line = fset.Position(id.Pos()).Line
				into[id.Name] = d
			}
		}
	}
	return nil
}

// declNames returns the names that a top-level declaration declares in the
// package scope: methods and imports declare none there.
func declNames(d ast.Decl) []*ast.Ident {
	var names []*ast.Ident
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil {
			names = append(names, d.Name)
		}
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name)
			case *ast.ValueSpec:
				names = append(names, s.Names...)
			}
		}
	}
	return names
}

// gone returns, for each old directory, the exported declarations that left
// it and that it no longer declares, by import path: a qualified use of one
// does not compile. A name that a bridge declares again still does.
func (sc *splitScope) gone(module string) watch {
	w := newWatch()
	for old, names := range sc.moved {
		for name, md := range names {
			w.bare[old] = mapsAdd(w.bare[old], name)
			if _, still := sc.decls[old][name]; !still && token.IsExported(name) && !md.test {
				w.addVia(module+"/"+old, sc.names[old], name)
			}
		}
	}
	return w
}

// mapsAdd adds key to the set m, which it makes when nil, and returns it.
func mapsAdd(m map[string]bool, key string) map[string]bool {
	if m == nil {
		m = map[string]bool{}
	}
	m[key] = true
	return m
}

// splitImporters returns the directories outside the split that hold a Go
// file whose syntax uses, through the import of an old directory, a name in
// w.via, and every Go file outside the split that imports an old directory.
// Only the files that use such a name need type information; finding them by
// syntax keeps the loads small when bridges keep every name resolving.
func (t *tree) splitImporters(sc *splitScope, w watch) ([]string, []string, error) {
	if len(sc.oldDirs) == 0 {
		return nil, nil, nil
	}
	patterns := make([]string, len(sc.oldDirs))
	for i, dir := range sc.oldDirs {
		patterns[i] = `"` + t.module + "/" + dir + `"`
	}
	found, err := t.git.grepFiles(patterns)
	if err != nil {
		return nil, nil, err
	}
	var (
		dirs, files []string
		via         = watch{via: w.via}
	)
	for _, f := range found {
		dir := path.Dir(f)
		if !strings.HasSuffix(f, ".go") || !inPackage(f) ||
			slices.Contains(sc.oldDirs, dir) || slices.Contains(sc.newDirs, dir) {
			continue
		}
		files = append(files, f)
		src, err := t.read(f)
		if err != nil {
			return nil, nil, err
		}
		// A file that does not parse yields what it can, an empty file at
		// worst; its own build reports it more clearly than fix could.
		parsed, _ := parser.ParseFile(token.NewFileSet(), f, src, parser.SkipObjectResolution)
		if pos, _ := via.use(parsed, dir); pos.IsValid() && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	return dirs, files, nil
}

// fixRun is one run of fix -ticket.
type fixRun struct {
	t       *tree
	sc      *splitScope
	plan    *fixPlan
	bridges *bridges
}

// planFix plans fix -ticket: each reference that the ticket's file moves
// broke is pointed at the package that now declares what it names, each use
// of a bridge in a moved file at the bridge's target, the files in external
// become external tests, and what fix cannot do is reported.
func (t *tree) planFix(ticket string, external []string) (*fixPlan, error) {
	moves, err := t.ticketMoves(ticket, len(external) > 0)
	if err != nil {
		return nil, err
	}
	if external, err = t.externalFiles(external); err != nil {
		return nil, err
	}
	sc, err := t.splitScope(moves)
	if err != nil {
		return nil, err
	}
	w := sc.gone(t.module)
	importers, candidates, err := t.splitImporters(sc, w)
	if err != nil {
		return nil, err
	}
	scope := slices.Concat(sc.oldDirs, sc.newDirs)
	slices.Sort(scope)
	scope = slices.Compact(scope)
	dirs := slices.Concat(scope, importers)
	for _, f := range external {
		dirs = append(dirs, path.Dir(f))
	}
	slices.Sort(dirs)
	loads, err := t.loadPackages(slices.Compact(dirs))
	if err != nil {
		return nil, err
	}
	r := &fixRun{t: t, sc: sc, plan: newFixPlan(), bridges: newBridges()}
	for _, f := range external {
		r.plan.external[f] = true
	}
	compiled := t.compiled(loads)
	for rel, tf := range compiled {
		if ledger.IsBridge(path.Base(rel)) && !strings.HasSuffix(rel, "_test.go") &&
			slices.Contains(sc.oldDirs, path.Dir(rel)) {
			r.bridges.index(rel, tf)
		}
	}
	for _, pkgs := range loads {
		for _, pkg := range pkgs {
			r.undefined(pkg)
		}
	}
	for _, m := range moves {
		if tf := compiled[m.to]; tf != nil {
			r.plan.pointUses(m.to, tf, r.bridges)
		} else {
			r.plan.report(
				m.to,
				1,
				"no load compiles this file (GOOS linux, darwin and windows, -tags=e2e), "+
					"so fix cannot see what it uses from %s; check it by hand",
				path.Dir(m.from),
			)
		}
	}
	for _, f := range external {
		r.external(f, loads)
	}
	if err := t.reportSources(r.plan, scope); err != nil {
		return nil, err
	}
	for _, dir := range sc.oldDirs {
		candidates = append(candidates, t.goFilesIn(dir)...)
	}
	if err := t.uncompiled(r.plan, candidates, compiled, w); err != nil {
		return nil, err
	}
	r.plan.resolve()
	if err := t.reportCycles(r.plan); err != nil {
		return nil, err
	}
	return r.plan, nil
}

// undefined plans the repair of each identifier that the type checker could
// not resolve in pkg, one variant of a package, and that a file move broke.
func (r *fixRun) undefined(pkg *packages.Package) {
	files := map[string]*ast.File{}
	for _, f := range pkg.Syntax {
		files[pkg.Fset.File(f.Pos()).Name()] = f
	}
	for _, e := range pkg.TypeErrors {
		pos := pkg.Fset.Position(e.Pos)
		f := files[pos.Filename]
		rel, ok := r.t.rel(pos.Filename)
		if !strings.HasPrefix(e.Msg, "undefined: ") || f == nil || !ok {
			continue
		}
		tf := &typedFile{file: f, fset: pkg.Fset, info: pkg.TypesInfo, pkg: pkg.Types}
		syn := r.plan.syntaxOf(tf)
		id := syn.at[pos.Offset]
		switch {
		case id == nil:
			// The checker puts "undefined:" on the identifier; anywhere else
			// is no name that fix could point.
		case syn.sel[id] != nil:
			r.qualified(rel, tf, syn.sel[id])
		default:
			r.bare(rel, tf, syn, id)
		}
	}
}

// qualified plans the repair of old.X, where X left the old directory for
// another: the qualifier names the new package, or goes in a file of that
// package. An X that an in-package test file took along is in sight only of
// the external tests in its new directory, as an export_test.go is written
// for; anywhere else no import reaches it.
func (r *fixRun) qualified(rel string, tf *typedFile, sel *ast.SelectorExpr) {
	// The type checker reports "undefined: q.X" only where q names an
	// import.
	q, _ := sel.X.(*ast.Ident)
	pn, ok := tf.info.Uses[q].(*types.PkgName)
	if !ok {
		return
	}
	via := pn.Imported().Path()
	old, _ := strings.CutPrefix(via, r.t.module+"/")
	md, ok := r.sc.moved[old][sel.Sel.Name]
	switch {
	case !ok || md.external:
	case md.test && (md.dir != path.Dir(rel) || !strings.HasSuffix(tf.file.Name.Name, "_test")):
		r.plan.report(
			rel,
			tf.line(sel.Pos()),
			"%s.%s moved with the test file %s, which %s cannot import; move it into a file that is no test",
			q.Name,
			sel.Sel.Name,
			md.file,
			path.Dir(rel),
		)
	default:
		r.point(rel, tf, sel.Pos(), sel.End(), md.dir, sel.Sel.Name, via)
	}
}

// bare plans the repair of an unqualified X that a file move separated from
// its declaration: in a moved file, X stayed in the old directory or left
// for another; in a file that stayed, X left. An exported X gets the
// qualifier of its package; anything else is reported.
func (r *fixRun) bare(rel string, tf *typedFile, syn *syntax, id *ast.Ident) {
	dir := path.Dir(rel)
	origin := dir
	if old, ok := r.sc.from[rel]; ok {
		origin = path.Dir(old)
	}
	target, d, ok := r.sc.find(origin, dir, id.Name, strings.HasSuffix(tf.file.Name.Name, "_test"))
	if !ok {
		return
	}
	line := tf.line(id.Pos())
	switch method := syn.recv[id]; {
	case method != nil:
		r.plan.report(
			rel,
			line,
			"the method %s is in %s, but its receiver type %s is in %s (%s); move the type and its methods together",
			method.Name.Name,
			dir,
			id.Name,
			target,
			d.file,
		)
	case d.test:
		r.plan.report(rel, line, "%s is declared in the test file %s, which %s cannot import; "+
			"move it too, or declare it again", id.Name, d.file, dir)
	case !token.IsExported(id.Name):
		r.plan.report(rel, line, "%s is unexported in %s (%s), so %s cannot use it; "+
			"export it, or move it with the file", id.Name, target, d.file, dir)
	case d.bridge && r.bridges.byName[target][id.Name] != nil &&
		r.bridges.byName[target][id.Name].problem == "":
		bt := r.bridges.byName[target][id.Name]
		r.plan.refer(rel, tf, reference{
			start:   tf.offset(id.Pos()),
			end:     tf.offset(id.End()),
			pkgPath: bt.pkgPath,
			pkgName: bt.pkgName,
			name:    bt.name,
		})
	default:
		r.point(rel, tf, id.Pos(), id.End(), target, id.Name, "")
	}
}

// find returns where the name used in a file directly in dir, which came
// from origin, is declared now: in origin, when the file moved away from it,
// or in the directory another moved file took it to. external says whether
// the file is in an external test package, which sees the declarations of
// such files only.
func (sc *splitScope) find(origin, dir, name string, external bool) (string, decl, bool) {
	if d, ok := sc.decls[origin][name]; ok && origin != dir && d.external == external {
		return origin, d, true
	}
	if md, ok := sc.moved[origin][name]; ok && md.dir != dir && md.external == external {
		return md.dir, md.decl, true
	}
	return "", decl{}, false
}

// point plans the reference from start to end in the file rel to name in the
// package of dir: qualified, or bare in a file of that package. A name that
// fix points at is declared in a file that is no test, whose clause gave the
// package its name.
func (r *fixRun) point(rel string, tf *typedFile, start, end token.Pos, dir, name, via string) {
	r.plan.refer(rel, tf, reference{
		start:   tf.offset(start),
		end:     tf.offset(end),
		pkgPath: r.t.module + "/" + dir,
		pkgName: r.sc.names[dir],
		name:    name,
		via:     via,
	})
}
