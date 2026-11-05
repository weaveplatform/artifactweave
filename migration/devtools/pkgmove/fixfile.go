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
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
)

// reference is one identifier, bare or qualified, that fix points at the
// package that now declares what it names.
type reference struct {
	// start and end are the bytes the reference replaces: the identifier,
	// or the whole selector of a qualified one.
	start, end int
	// pkgPath is the package that declares the target, and pkgName its
	// name; pkgPath is empty when that is the file's own package, which
	// needs no qualifier.
	pkgPath, pkgName string
	name             string
	// via is the import path of the qualifier that the reference replaces,
	// if any: an import whose every qualifier is replaced goes.
	via string
}

// importRef is one import of a file: its explicit name, empty when it has
// none, and its path.
type importRef struct{ name, path string }

// goFix is what fix changes in one Go file.
type goFix struct {
	path string
	// tf is the first load that compiled the file. Every load compiles the
	// same text, so a byte offset holds in each.
	tf   *typedFile
	tok  *token.File
	refs map[int]reference
	// clause is the new package clause, or empty when it stays.
	clause string
	// adds maps the path of each import the change adds to its explicit
	// name, and drops are the imports it removes; resolve fills both.
	adds   map[string]string
	drops  []importRef
	es     edits
	counts editCounts
}

// problem is something fix leaves for hand work, printed as file:line:
// message.
type problem struct {
	file string
	line int
	msg  string
}

func (p problem) String() string { return fmt.Sprintf("%s:%d: %s", p.file, p.line, p.msg) }

// fixPlan is everything a fix run plans: the change to each Go file and the
// problems it leaves.
type fixPlan struct {
	files    map[string]*goFix
	problems map[problem]bool
	// external holds the files that become external tests.
	external map[string]bool
	notes    []string
	// syntax caches the syntax index of each parsed file; the test variants
	// of a package share the files of the package itself.
	syntax map[*ast.File]*syntax
}

func newFixPlan() *fixPlan {
	return &fixPlan{
		files:    map[string]*goFix{},
		problems: map[problem]bool{},
		external: map[string]bool{},
		syntax:   map[*ast.File]*syntax{},
	}
}

// report records a problem in the file rel.
func (p *fixPlan) report(rel string, line int, format string, args ...any) {
	p.problems[problem{file: rel, line: line, msg: fmt.Sprintf(format, args...)}] = true
}

// sortedProblems returns the problems by file, line and message.
func (p *fixPlan) sortedProblems() []problem {
	return slices.SortedFunc(maps.Keys(p.problems), func(a, b problem) int {
		return cmp.Or(strings.Compare(a.file, b.file), cmp.Compare(a.line, b.line),
			strings.Compare(a.msg, b.msg))
	})
}

// file returns the change of the file rel, which tf compiles.
func (p *fixPlan) file(rel string, tf *typedFile) *goFix {
	g := p.files[rel]
	if g == nil {
		g = &goFix{path: rel, tf: tf, tok: tf.fset.File(tf.file.Pos()), refs: map[int]reference{}}
		p.files[rel] = g
	}
	return g
}

// refer plans the reference r in the file rel, which tf compiles; a target
// in the file's own package, which the file stays in, goes bare. A generated
// file is never edited but regenerated (decision 0002), so a reference there
// is a problem instead.
func (p *fixPlan) refer(rel string, tf *typedFile, r reference) {
	if ast.IsGenerated(tf.file) {
		p.report(rel, tf.fset.Position(tf.file.Package).Line,
			"the file is generated, so fix leaves it: change its generator and run make generate")
		return
	}
	if p.own(rel, tf, r.pkgPath) {
		r.pkgPath = ""
	}
	g := p.file(rel, tf)
	if _, ok := g.refs[r.start]; !ok {
		g.refs[r.start] = r
	}
}

// offset returns the byte offset of pos in the file of tf.
func (tf *typedFile) offset(pos token.Pos) int { return tf.fset.File(pos).Offset(pos) }

// line returns the line of pos in the file of tf.
func (tf *typedFile) line(pos token.Pos) int { return tf.fset.Position(pos).Line }

// resolve decides the edits and imports of g. Each target package gets a
// name: that of an import the file has, else the package's own name, else
// the first of <parent><name>, <name>2, <name>3 and so on that means nothing
// else at any use and that no other import takes. An import whose every
// qualifier the change replaces goes, and its name is free again.
func (g *goFix) resolve() {
	replaced := map[string]int{}
	byPath := map[string][]reference{}
	for _, off := range slices.Sorted(maps.Keys(g.refs)) {
		r := g.refs[off]
		byPath[r.pkgPath] = append(byPath[r.pkgPath], r)
		if r.via != "" {
			replaced[r.via]++
		}
	}
	for _, spec := range g.tf.file.Imports {
		p, _ := strconv.Unquote(spec.Path.Value)
		if n := replaced[p]; n > 0 && n == g.uses(spec) {
			g.drops = append(g.drops, importRef{name: explicitName(spec), path: p})
		}
	}
	g.adds = map[string]string{}
	for _, p := range slices.Sorted(maps.Keys(byPath)) {
		refs := byPath[p]
		name := ""
		if p != "" {
			def := refs[0].pkgName
			var add bool
			if name, add = g.importName(p, def, refs); add && name != def {
				g.adds[p] = name
			} else if add {
				g.adds[p] = ""
			}
		}
		for _, r := range refs {
			text := r.name
			if name != "" {
				text = name + "." + r.name
			}
			g.es = append(g.es, edit{start: r.start, end: r.end, text: text})
		}
	}
	if g.clause != "" {
		off := g.tf.offset(g.tf.file.Name.Pos())
		g.es = append(g.es, edit{start: off, end: off + len(g.tf.file.Name.Name), text: g.clause})
		g.counts.clauses = 1
	}
	g.counts.qualifiers = len(g.refs)
	g.counts.imports = len(g.adds)
	g.counts.unused = len(g.drops)
}

// explicitName returns the name an import spec gives, or empty.
func explicitName(spec *ast.ImportSpec) string {
	if spec.Name == nil {
		return ""
	}
	return spec.Name.Name
}

// pkgNameOf returns the object an import spec declares, or nil when the
// load could not resolve it.
func (g *goFix) pkgNameOf(spec *ast.ImportSpec) *types.PkgName {
	obj := g.tf.info.Implicits[spec]
	if spec.Name != nil {
		obj = g.tf.info.Defs[spec.Name]
	}
	pn, _ := obj.(*types.PkgName)
	return pn
}

// uses returns how many qualifiers of the file refer to the import spec;
// none when the load could not resolve it.
func (g *goFix) uses(spec *ast.ImportSpec) int {
	pn := g.pkgNameOf(spec)
	n := 0
	ast.Inspect(g.tf.file, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && pn != nil && g.tf.info.Uses[id] == pn {
			n++
		}
		return true
	})
	return n
}

// localName returns the name by which the file refers to an import: the
// name the load gives it, explicit or the package's own, else the last
// element of its path.
func (g *goFix) localName(spec *ast.ImportSpec) string {
	if pn := g.pkgNameOf(spec); pn != nil {
		return pn.Name()
	}
	p, _ := strconv.Unquote(spec.Path.Value)
	return path.Base(p)
}

// dropped reports whether the change removes the import that obj declares.
func (g *goFix) dropped(obj types.Object) bool {
	pn, ok := obj.(*types.PkgName)
	return ok && slices.ContainsFunc(g.drops, func(d importRef) bool {
		return d.path == pn.Imported().Path() && (d.name == "" || d.name == pn.Name())
	})
}

// importName returns the name the file refers to pkgPath by at refs, and
// whether the file must import it.
func (g *goFix) importName(pkgPath, def string, refs []reference) (string, bool) {
	positions := make([]token.Pos, len(refs))
	for i, r := range refs {
		positions[i] = g.tok.Pos(r.start)
	}
	taken := map[string]bool{}
	for _, spec := range g.tf.file.Imports {
		name := g.localName(spec)
		p, _ := strconv.Unquote(spec.Path.Value)
		switch {
		case slices.Contains(g.drops, importRef{name: explicitName(spec), path: p}):
		case p == pkgPath && name != "_" && name != "." && g.means(name, positions, pkgPath):
			return name, false
		default:
			taken[name] = true
		}
	}
	parent := identifier(path.Base(path.Dir(pkgPath)))
	for i := 0; ; i++ {
		name := def
		switch {
		case i == 1:
			name = parent + def
		case i > 1:
			name = def + strconv.Itoa(i)
		}
		if !taken[name] && g.means(name, positions, "") {
			return name, true
		}
	}
}

// means reports whether name, at each of positions, means the import of
// pkgPath, or with pkgPath empty means nothing that the change keeps.
func (g *goFix) means(name string, positions []token.Pos, pkgPath string) bool {
	for _, pos := range positions {
		// Each position lies in a file of the package, so some file scope
		// holds it.
		_, obj := g.tf.pkg.Scope().Innermost(pos).LookupParent(name, pos)
		pn, isImport := obj.(*types.PkgName)
		switch {
		case pkgPath != "" && (!isImport || pn.Imported().Path() != pkgPath):
			return false
		case pkgPath == "" && obj != nil && !g.dropped(obj):
			return false
		}
	}
	return true
}

// identifier returns s with every byte that cannot be in a Go identifier
// left out, in lower case, for an import name made from a directory.
func identifier(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '_', r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimLeftFunc(s, unicode.IsDigit))
}

// apply returns the new content of the file src, in module: the edits made,
// the imports added and removed, and the result formatted. A changed import
// block is grouped as goimports groups it, standard library first and the
// module's own packages last, which is what the formatters of .golangci.yml
// keep; make fmt then has nothing to move.
func (g *goFix) apply(src []byte, module string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, g.path, g.es.apply(src), parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: %s does not parse after the fix: %w",
			errUnexpected,
			g.path,
			err,
		)
	}
	var b bytes.Buffer
	if len(g.adds)+len(g.drops) == 0 {
		// A file whose imports stay keeps their grouping; a file that parsed
		// always prints.
		_ = format.Node(&b, fset, f)
		return b.Bytes(), nil
	}
	before := map[*ast.GenDecl]int{}
	for _, d := range f.Decls {
		if gen, ok := d.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			before[gen] = len(gen.Specs)
		}
	}
	for _, d := range g.drops {
		astutil.DeleteNamedImport(fset, f, d.name, d.path)
	}
	for _, p := range slices.Sorted(maps.Keys(g.adds)) {
		astutil.AddNamedImport(fset, f, g.adds[p], p)
	}
	// A block that a removal leaves with one import loses its parentheses,
	// as it would have been written with one import; astutil keeps them.
	for gen, n := range before {
		if len(gen.Specs) != 1 || n == 1 {
			continue
		}
		if spec := gen.Specs[0].(*ast.ImportSpec); spec.Doc == nil && spec.Comment == nil {
			gen.Lparen, gen.Rparen = token.NoPos, token.NoPos
		}
	}
	_ = format.Node(&b, fset, f)
	// imports takes the local prefix only as a package variable; a process
	// runs one command over one module, so every write here is the same.
	imports.LocalPrefix = module
	// FormatOnly neither adds nor removes an import, so nothing is looked up
	// beyond the file.
	formatted, err := imports.Process(g.path, b.Bytes(), &imports.Options{
		Comments: true, TabIndent: true, TabWidth: 8, FormatOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: format %s: %w", errUnexpected, g.path, err)
	}
	return formatted, nil
}

// syntax locates the identifiers of one parsed file and what stands around
// them.
type syntax struct {
	// at holds the identifiers by byte offset.
	at map[int]*ast.Ident
	// sel maps the Sel identifier of each selector to the selector.
	sel map[*ast.Ident]*ast.SelectorExpr
	// recv maps the base type identifier of each method receiver to the
	// method.
	recv map[*ast.Ident]*ast.FuncDecl
	// embedded holds the type identifier of each embedded struct field: the
	// field takes its name from it.
	embedded map[*ast.Ident]bool
	// assigned holds each identifier that is assigned to, incremented or has
	// its address taken: the Sel of a selector, else the identifier.
	assigned map[*ast.Ident]bool
}

// syntaxOf returns the syntax index of the file of tf.
func (p *fixPlan) syntaxOf(tf *typedFile) *syntax {
	if s := p.syntax[tf.file]; s != nil {
		return s
	}
	s := &syntax{
		at:       map[int]*ast.Ident{},
		sel:      map[*ast.Ident]*ast.SelectorExpr{},
		recv:     map[*ast.Ident]*ast.FuncDecl{},
		embedded: map[*ast.Ident]bool{},
		assigned: map[*ast.Ident]bool{},
	}
	ast.Inspect(tf.file, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.Ident:
			s.at[tf.offset(x.Pos())] = x
		case *ast.SelectorExpr:
			s.sel[x.Sel] = x
		case *ast.FuncDecl:
			if x.Recv != nil && len(x.Recv.List) == 1 {
				if id := baseIdent(x.Recv.List[0].Type); id != nil {
					s.recv[id] = x
				}
			}
		case *ast.StructType:
			for _, f := range x.Fields.List {
				if id := baseIdent(f.Type); len(f.Names) == 0 && id != nil {
					s.embedded[id] = true
				}
			}
		case *ast.AssignStmt:
			for _, e := range x.Lhs {
				s.assign(e)
			}
		case *ast.IncDecStmt:
			s.assign(x.X)
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				s.assign(x.X)
			}
		}
		return true
	})
	p.syntax[tf.file] = s
	return s
}

func (s *syntax) assign(e ast.Expr) {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		s.assigned[x] = true
	case *ast.SelectorExpr:
		s.assigned[x.Sel] = true
	}
}

// baseIdent returns the identifier that names the type of e: e itself, the
// Sel of a qualified name, below any pointer, parentheses and type
// arguments; nil for any other type expression.
func baseIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.SelectorExpr:
			return x.Sel
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		default:
			return nil
		}
	}
}
