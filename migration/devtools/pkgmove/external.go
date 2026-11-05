package pkgmove

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"strings"

	"golang.org/x/tools/go/packages"
)

// externalFiles checks the arguments of -external, root-relative paths of
// tracked test files in package directories, and returns them cleaned. A
// file that is an external test already is refused.
func (t *tree) externalFiles(args []string) ([]string, error) {
	files := make([]string, 0, len(args))
	for _, a := range args {
		p := path.Clean(strings.ReplaceAll(a, `\`, "/"))
		if !t.tracked[p] || !strings.HasSuffix(p, "_test.go") || !inPackage(p) {
			return nil, fmt.Errorf(
				"%w: -external: %s is not a tracked test file of a package",
				errUsage,
				a,
			)
		}
		src, err := t.read(p)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.PackageClauseOnly)
		if err != nil {
			return nil, fmt.Errorf("read the package clause: %w", err)
		}
		if strings.HasSuffix(f.Name.Name, "_test") {
			return nil, fmt.Errorf("%w: %s is an external test already", errPrecondition, p)
		}
		files = append(files, p)
	}
	return files, nil
}

// external plans the conversion of the in-package test file rel to the
// package's external test package, the way out of an import cycle that the
// in-package form would close (rule 14 of #935): the clause becomes
// <name>_test, and each exported package-level declaration it uses gets the
// package's qualifier and import. What the conversion cannot carry is
// reported: each unexported declaration, field or method it uses, which
// needs an exported name in export_test.go; each method it declares; and
// each of its package-level declarations that another in-package file uses,
// which takes the fields and methods of a type along.
func (r *fixRun) external(rel string, loads [][]*packages.Package) {
	pkg, f := r.inPackageVariant(rel, loads)
	if f == nil {
		r.plan.report(
			rel,
			1,
			"no load compiles this file (GOOS linux, darwin and windows, -tags=e2e), "+
				"so fix cannot convert it; convert it by hand",
		)
		return
	}
	tf := &typedFile{file: f, fset: pkg.Fset, info: pkg.TypesInfo, pkg: pkg.Types}
	g := r.plan.file(rel, tf)
	g.clause = pkg.Types.Name() + "_test"
	self := pkg.Fset.File(f.Pos())
	here := func(obj types.Object) bool { return pkg.Fset.File(obj.Pos()) == self }
	reported := map[string]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		obj := tf.info.Uses[id]
		if !ok || obj == nil || obj.Pkg() != pkg.Types || here(obj) {
			return true
		}
		switch kind := objectKind(obj); {
		case obj.Exported() && obj.Parent() == pkg.Types.Scope():
			r.plan.refer(rel, tf, reference{
				start:   tf.offset(id.Pos()),
				end:     tf.offset(id.End()),
				pkgPath: pkg.Types.Path(),
				pkgName: pkg.Types.Name(),
				name:    id.Name,
			})
		case !obj.Exported() && !reported[kind+" "+id.Name]:
			reported[kind+" "+id.Name] = true
			r.plan.report(rel, tf.line(id.Pos()),
				"uses the unexported %s %s, which package %s_test cannot see; "+
					"give it an exported name in export_test.go", kind, id.Name, pkg.Types.Name())
		}
		return true
	})
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil {
			r.plan.report(
				rel,
				tf.line(fd.Pos()),
				"declares the method %s, which an external test cannot; move it to an in-package test file",
				fd.Name.Name,
			)
		}
	}
	for _, other := range pkg.Syntax {
		orel, ok := r.t.rel(pkg.Fset.File(other.Pos()).Name())
		if other == f || !ok {
			continue
		}
		seen := map[string]bool{}
		ast.Inspect(other, func(node ast.Node) bool {
			id, ok := node.(*ast.Ident)
			obj := pkg.TypesInfo.Uses[id]
			if ok && obj != nil && here(obj) && obj.Parent() == pkg.Types.Scope() &&
				!seen[id.Name] {
				seen[id.Name] = true
				r.plan.report(
					orel,
					pkg.Fset.Position(id.Pos()).Line,
					"uses %s, which %s declares; once that file is an external test this one cannot see it, "+
						"so move %s here or to a shared in-package file",
					id.Name,
					rel,
					id.Name,
				)
			}
			return true
		})
	}
}

// inPackageVariant returns the first package of loads that compiles the file
// rel as part of its package, which for a test file is the package's test
// variant, with the file's syntax there; nil when none does.
func (r *fixRun) inPackageVariant(
	rel string,
	loads [][]*packages.Package,
) (*packages.Package, *ast.File) {
	for _, pkgs := range loads {
		for _, p := range pkgs {
			if strings.HasSuffix(p.Types.Name(), "_test") {
				continue
			}
			for _, f := range p.Syntax {
				if frel, ok := r.t.rel(p.Fset.File(f.Pos()).Name()); ok && frel == rel {
					return p, f
				}
			}
		}
	}
	return nil, nil
}

// objectKind names the kind of a declaration for a message.
func objectKind(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Func:
		if o.Signature().Recv() != nil {
			return "method"
		}
		return "func"
	case *types.Var:
		if o.IsField() {
			return "field"
		}
		return "var"
	case *types.Const:
		return "const"
	case *types.TypeName:
		return "type"
	}
	return "identifier"
}
