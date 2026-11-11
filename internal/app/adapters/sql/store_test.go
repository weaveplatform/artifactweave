package sql_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Store embeds *dialect.Store, which promotes the dialect store's exported
// methods onto it. A method Store declares under the same name shadows the
// promoted one without a compile error: callers of Store reach the outer
// method, while code that holds the *dialect.Store reaches the inner one. So
// the dialect store exports no method named like an accessor of the
// composite contract's store (appstoretest.Store) except Tx, AfterCommit
// and Close, which Store takes from it on purpose. Reflection cannot tell a
// method Store declares from one promoted onto it, so the methods Store
// declares, including those outside the contract such as Reseal, are read
// from this package's source.
func TestUnitDialectStoreShadowsNoAccessor(t *testing.T) {
	inner := reflect.TypeFor[*dialect.Store]()
	fromDialect := []string{"Tx", "AfterCommit", "Close"}
	for _, name := range fromDialect {
		if _, ok := inner.MethodByName(name); !ok {
			t.Errorf("*dialect.Store has no %s method for Store to take", name)
		}
	}
	names := declaredStoreMethods(t)
	if !slices.Contains(names, "Reseal") {
		t.Fatal("found no Reseal method on Store; the scan is broken")
	}
	contract := reflect.TypeFor[appstoretest.Store]()
	for i := range contract.NumMethod() {
		name := contract.Method(i).Name
		if !slices.Contains(fromDialect, name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if _, ok := inner.MethodByName(name); ok {
			t.Errorf("*dialect.Store has a %s method, which Store's %[1]s shadows", name)
		}
	}
}

// declaredStoreMethods returns the exported methods this package's non-test
// files declare on Store.
func declaredStoreMethods(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Store" {
				names = append(names, fn.Name.Name)
			}
		}
	}
	return names
}
