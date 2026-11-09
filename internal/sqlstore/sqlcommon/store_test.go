package sqlcommon_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	artport "github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	fedport "github.com/weaveplatform/artifactweave/migration/federation/core/port"
	ntfport "github.com/weaveplatform/artifactweave/migration/notifications/core/port"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	relport "github.com/weaveplatform/artifactweave/migration/releases/core/port"
	retport "github.com/weaveplatform/artifactweave/migration/retention/core/port"
	wrkport "github.com/weaveplatform/artifactweave/migration/workers/core/port"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Store embeds *dialect.Store, which promotes the dialect store's exported
// methods onto it. A method Store declares under the same name shadows the
// promoted one without a compile error: callers of Store reach the outer
// method, while code that holds the *dialect.Store reaches the inner one. So
// the dialect store exports no method named like an aggregate accessor
// except Tx, AfterCommit and Close, which Store takes from it on purpose.
// Reflection cannot tell a method Store declares from one promoted onto it,
// so the methods Store declares, including those outside the aggregate such
// as Reseal, are read from this package's source.
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
	aggregate := reflect.TypeFor[store.Store]()
	for i := range aggregate.NumMethod() {
		name := aggregate.Method(i).Name
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

// The SQL store, the composite the SQLite and PostgreSQL stores embed, must
// satisfy each module's port Store, which declares only the part of the
// metadata store that module uses, so a module can take this store as its own
// until its adapters move (STO-14, STO-15, STO-17, STO-18). The aggregate's
// accessors return the bridged port interfaces, which are the same types.
var (
	_ artport.Store = (*sqlcommon.Store)(nil)
	_ ntfport.Store = (*sqlcommon.Store)(nil)
	_ retport.Store = (*sqlcommon.Store)(nil)
	_ fedport.Store = (*sqlcommon.Store)(nil)
	_ relport.Store = (*sqlcommon.Store)(nil)
	_ wrkport.Store = (*sqlcommon.Store)(nil)
)
