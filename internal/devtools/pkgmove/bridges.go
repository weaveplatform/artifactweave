package pkgmove

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/devtools/ledger"
)

// bridgeTarget is the declaration that a bridge declaration aliases.
type bridgeTarget struct {
	pkgPath, pkgName, name string
	// problem says why fix cannot point a use of the bridge at a target;
	// empty when it can.
	problem string
	// variable marks a var bridge: assigning to it does not assign to its
	// target, so such a use is left for hand work.
	variable bool
}

// bridges indexes the declarations of a set of bridge files.
type bridges struct {
	// at maps the file system path and byte offset of each declared name,
	// as "path:offset", to its target; every load agrees on both.
	at map[string]*bridgeTarget
	// byName maps a root-relative directory and a name to the target of the
	// bridge that declares the name there.
	byName map[string]map[string]*bridgeTarget
	// names maps each directory that holds bridge files to its package's
	// name.
	names map[string]string
}

func newBridges() *bridges {
	return &bridges{
		at:     map[string]*bridgeTarget{},
		byName: map[string]map[string]*bridgeTarget{},
		names:  map[string]string{},
	}
}

// index adds the package-level declarations of the bridge file rel, which tf
// compiles. A declaration that is no alias, such as a type of its own or a
// function that does more than call another, gets a problem.
func (b *bridges) index(rel string, tf *typedFile) {
	for _, d := range tf.file.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					b.add(rel, tf, s.Name, typeTarget(tf.info, s))
				case *ast.ValueSpec:
					for i, name := range s.Names {
						b.add(rel, tf, name, valueTarget(tf.info, d.Tok, s, i))
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv == nil {
				b.add(rel, tf, d.Name, wrapperTarget(tf.info, d))
			}
		}
	}
	b.names[path.Dir(rel)] = tf.pkg.Name()
}

func (b *bridges) add(rel string, tf *typedFile, name *ast.Ident, target *bridgeTarget) {
	if name.Name == "_" {
		return
	}
	pos := tf.fset.Position(name.Pos())
	b.at[fmt.Sprintf("%s:%d", pos.Filename, pos.Offset)] = target
	dir := path.Dir(rel)
	if b.byName[dir] == nil {
		b.byName[dir] = map[string]*bridgeTarget{}
	}
	b.byName[dir][name.Name] = target
}

// lookup returns the target of the bridge declaration obj, or nil when obj
// is no bridge declaration.
func (b *bridges) lookup(fset *token.FileSet, obj types.Object) *bridgeTarget {
	if obj == nil || !obj.Pos().IsValid() {
		return nil
	}
	pos := fset.Position(obj.Pos())
	return b.at[fmt.Sprintf("%s:%d", pos.Filename, pos.Offset)]
}

// targetOf returns the bridge target of obj, a package-level declaration.
func targetOf(obj types.Object) *bridgeTarget {
	return &bridgeTarget{pkgPath: obj.Pkg().Path(), pkgName: obj.Pkg().Name(), name: obj.Name()}
}

// noTarget returns a target with a problem.
func noTarget(format string, args ...any) *bridgeTarget {
	return &bridgeTarget{problem: fmt.Sprintf(format, args...)}
}

// referent returns the package-level declaration that e names: an
// identifier or a qualified one, in parentheses or not; nil for any other
// expression.
func referent(info *types.Info, e ast.Expr) types.Object {
	var id *ast.Ident
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		if q, ok := x.X.(*ast.Ident); !ok || !isPkgName(info.Uses[q]) {
			return nil
		}
		id = x.Sel
	default:
		return nil
	}
	obj := info.Uses[id]
	if obj == nil || obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
		return nil
	}
	return obj
}

func isPkgName(obj types.Object) bool {
	_, ok := obj.(*types.PkgName)
	return ok
}

// typeArgs splits an instantiation into the generic name and its type
// arguments; any other expression has none.
func typeArgs(e ast.Expr) (ast.Expr, []ast.Expr) {
	switch x := e.(type) {
	case *ast.IndexExpr:
		return x.X, []ast.Expr{x.Index}
	case *ast.IndexListExpr:
		return x.X, x.Indices
	}
	return e, nil
}

// passesThrough reports whether args are exactly the type parameters of
// params, in order: the instantiation forwards every parameter unchanged.
func passesThrough(info *types.Info, params *ast.FieldList, args []ast.Expr) bool {
	var names []*ast.Ident
	if params != nil {
		for _, f := range params.List {
			names = append(names, f.Names...)
		}
	}
	if len(names) != len(args) {
		return false
	}
	for i, a := range args {
		id, ok := a.(*ast.Ident)
		if !ok || info.Uses[id] == nil || info.Uses[id] != info.Defs[names[i]] {
			return false
		}
	}
	return true
}

// typeTarget returns the target of a type declaration in a bridge file:
// type X = pkg.Y, or type X[T any] = pkg.Y[T] with the parameters passed
// through.
func typeTarget(info *types.Info, s *ast.TypeSpec) *bridgeTarget {
	if !s.Assign.IsValid() {
		return noTarget("%s is a type of its own, not an alias", s.Name.Name)
	}
	generic, args := typeArgs(s.Type)
	obj, ok := referent(info, generic).(*types.TypeName)
	switch {
	case !ok:
		return noTarget("%s aliases no declared type", s.Name.Name)
	case !passesThrough(info, s.TypeParams, args):
		return noTarget(
			"%s aliases an instantiation of %s.%s",
			s.Name.Name,
			obj.Pkg().Name(),
			obj.Name(),
		)
	}
	return targetOf(obj)
}

// valueTarget returns the target of the i-th name of a var or const
// declaration in a bridge file: var X = pkg.Y, which may name a function,
// or const X = pkg.Y.
func valueTarget(info *types.Info, tok token.Token, s *ast.ValueSpec, i int) *bridgeTarget {
	name := s.Names[i].Name
	if s.Type != nil || len(s.Values) != len(s.Names) {
		return noTarget("%s is declared with a type or value of its own", name)
	}
	obj := referent(info, s.Values[i])
	switch obj.(type) {
	case *types.Var, *types.Func:
		if tok == token.VAR {
			t := targetOf(obj)
			t.variable = true
			return t
		}
	case *types.Const:
		if tok == token.CONST {
			return targetOf(obj)
		}
	}
	return noTarget("%s is not another package's %s under a new name", name, tok)
}

// wrapperTarget returns the target of a function in a bridge file, the form
// a generic function's bridge takes: a body that only calls the target with
// its own parameters and type parameters, and a signature identical to the
// target's.
func wrapperTarget(info *types.Info, d *ast.FuncDecl) *bridgeTarget {
	name := d.Name.Name
	wrapper, _ := info.Defs[d.Name].(*types.Func)
	call := forwardedCall(d)
	if wrapper == nil || call == nil {
		return noTarget("%s does more than call another function", name)
	}
	generic, targs := typeArgs(call.Fun)
	callee, ok := referent(info, generic).(*types.Func)
	switch {
	case !ok:
		return noTarget("%s does more than call another function", name)
	case !types.Identical(wrapper.Type(), callee.Type()):
		return noTarget("the signature of %s differs from that of %s.%s",
			name, callee.Pkg().Name(), callee.Name())
	case !forwards(info, wrapper.Signature(), call, targs):
		return noTarget("%s does not pass its parameters to %s.%s unchanged",
			name, callee.Pkg().Name(), callee.Name())
	}
	return targetOf(callee)
}

// forwardedCall returns the call that is the whole body of d: return f(...)
// or f(...).
func forwardedCall(d *ast.FuncDecl) *ast.CallExpr {
	if d.Body == nil || len(d.Body.List) != 1 {
		return nil
	}
	var e ast.Expr
	switch s := d.Body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(s.Results) == 1 {
			e = s.Results[0]
		}
	case *ast.ExprStmt:
		e = s.X
	}
	call, _ := e.(*ast.CallExpr)
	return call
}

// forwards reports whether call passes the parameters of sig in order, the
// variadic one spread, and its type parameters in order, from the first, when
// it names any.
func forwards(info *types.Info, sig *types.Signature, call *ast.CallExpr, targs []ast.Expr) bool {
	params := sig.Params()
	if len(call.Args) != params.Len() || call.Ellipsis.IsValid() != sig.Variadic() {
		return false
	}
	for i, a := range call.Args {
		if id, ok := a.(*ast.Ident); !ok || info.Uses[id] != params.At(i) {
			return false
		}
	}
	// Type arguments that name fewer than all the type parameters leave the
	// rest to inference from the forwarded parameters.
	tps := sig.TypeParams()
	for i, a := range targs {
		if id, ok := a.(*ast.Ident); !ok || i >= tps.Len() || info.Uses[id] != tps.At(i).Obj() {
			return false
		}
	}
	return true
}

// pointUses plans, in the file rel, which tf compiles, the rewrite of each
// use of a declaration of b to the declaration it aliases: pkg.X becomes
// target.Y, and a bare X in the bridged package itself target.Y.
func (p *fixPlan) pointUses(rel string, tf *typedFile, b *bridges) {
	syn := p.syntaxOf(tf)
	ast.Inspect(tf.file, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.SelectorExpr:
			q, ok := x.X.(*ast.Ident)
			if pn, isImport := tf.info.Uses[q].(*types.PkgName); ok && isImport {
				p.pointUse(rel, tf, syn, b, x.Sel, x.Pos(), pn.Imported().Path())
				return false
			}
		case *ast.Ident:
			p.pointUse(rel, tf, syn, b, x, x.Pos(), "")
		}
		return true
	})
}

// pointUse plans the rewrite of id, which starts at start (its qualifier
// when it has one, imported from via), if it uses a bridge declaration.
func (p *fixPlan) pointUse(
	rel string, tf *typedFile, syn *syntax, b *bridges, id *ast.Ident, start token.Pos, via string,
) {
	target := b.lookup(tf.fset, tf.info.Uses[id])
	if target == nil {
		return
	}
	line := tf.line(id.Pos())
	switch {
	case target.problem != "":
		p.report(
			rel,
			line,
			"%s is a bridge that fix cannot point at a target: %s",
			id.Name,
			target.problem,
		)
	case syn.embedded[id] && target.name != id.Name:
		p.report(rel, line,
			"the embedded field %s would become %s if it named %s.%s; rewrite it by hand",
			id.Name, target.name, target.pkgName, target.name)
	case syn.assigned[id] && target.variable:
		p.report(rel, line,
			"assigns to the var bridge %s, which is not assigning to %s.%s; rewrite it by hand",
			id.Name, target.pkgName, target.name)
	default:
		p.refer(rel, tf, reference{
			start:   tf.offset(start),
			end:     tf.offset(id.End()),
			pkgPath: target.pkgPath,
			pkgName: target.pkgName,
			name:    target.name,
			via:     via,
		})
	}
}

// own reports whether the file rel, which tf compiles, belongs to the package
// pkgPath and stays in it, so that it names the package's declarations bare.
func (p *fixPlan) own(rel string, tf *typedFile, pkgPath string) bool {
	return tf.pkg.Path() == pkgPath && !p.external[rel]
}

// planBridges plans fix -bridges: every use of a declaration in a bridge
// file, outside the bridge files, pointed at the declaration it aliases. The
// bridge files are the bridge.go and bridge_*.go files of the legacy
// packages; the loads cover their packages and every package that imports
// one. A use in a file that no load compiles is reported, found by its
// syntax.
func (t *tree) planBridges() (*fixPlan, error) {
	var files []string
	for _, f := range t.files {
		if ledger.IsBridge(path.Base(f)) && ledger.IsLegacy(path.Dir(f)) && inPackage(f) {
			files = append(files, f)
		}
	}
	plan := newFixPlan()
	if len(files) == 0 {
		plan.notes = append(plan.notes, "no legacy package holds a bridge.go or bridge_*.go file")
		return plan, nil
	}
	var dirs, patterns []string
	for _, f := range files {
		if dir := path.Dir(f); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
			patterns = append(patterns, `"`+t.module+"/"+dir+`"`)
		}
	}
	importers, err := t.git.grepFiles(patterns)
	if err != nil {
		return nil, err
	}
	candidates := slices.DeleteFunc(importers, func(f string) bool {
		return !strings.HasSuffix(f, ".go") || !inPackage(f)
	})
	loadDirs := slices.Clone(dirs)
	for _, f := range candidates {
		loadDirs = append(loadDirs, path.Dir(f))
	}
	slices.Sort(loadDirs)
	loads, err := t.loadPackages(slices.Compact(loadDirs))
	if err != nil {
		return nil, err
	}
	compiled := t.compiled(loads)
	b := newBridges()
	for _, f := range files {
		// A bridge test declares no alias; it only goes with the bridge.
		if tf := compiled[f]; tf != nil && !strings.HasSuffix(f, "_test.go") {
			b.index(f, tf)
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(compiled)) {
		if !ledger.IsBridge(path.Base(rel)) {
			plan.pointUses(rel, compiled[rel], b)
		}
	}
	w := newWatch()
	for dir, names := range b.byName {
		for name := range names {
			w.add(t.module, dir, b.names[dir], name)
		}
	}
	for _, dir := range dirs {
		candidates = append(candidates, t.goFilesIn(dir)...)
	}
	candidates = slices.DeleteFunc(
		candidates,
		func(f string) bool { return ledger.IsBridge(path.Base(f)) },
	)
	if err := t.uncompiled(plan, candidates, compiled, w); err != nil {
		return nil, err
	}
	plan.resolve()
	return plan, nil
}
