package sql_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// sealScan is what scanSeals finds under a root: each purpose that a call
// seals under, with the positions of its calls, and a line for each call
// whose purpose the scan cannot read. Positions are slash-separated and
// relative to the root.
type sealScan struct {
	purposes   map[string][]string
	unreadable []string
}

// The two lines scanSeals writes for a purpose it cannot read.
const (
	notLiteral = "%s: cannot read the purpose %s: " +
		"seal under a string literal or a string constant of the package"
	notConstant = "%s: cannot read the purpose %s: " +
		"it is not a string constant of the package"
)

// scanSeals reads the non-test Go files under root, walked with
// repopath.SkipDir, for calls of a function or method named Seal or seal
// with three arguments: the purpose, row id and value that
// dialect.Store.Seal and crypt.Keyring.Seal both take, and which decision
// 0017 authenticates a sealed value with. Calls of another shape, such as
// cipher.AEAD's Seal with four, are someone else's. A purpose that is a
// string literal, or a string constant that a non-test file of the call's
// directory declares, is read. One that is a parameter of an enclosing
// function is passed through by a wrapper, whose own callers are scanned,
// and is skipped. Any other purpose is unreadable, so that no store can
// seal under a purpose the scan cannot see.
func scanSeals(root string) (sealScan, error) {
	scan := sealScan{purposes: map[string][]string{}}
	type constRef struct{ dir, name, pos string }
	var refs []constRef
	fset := token.NewFileSet()
	at := func(p token.Pos) string {
		pos := fset.Position(p)
		rel, err := filepath.Rel(root, pos.Filename)
		if err != nil {
			rel = pos.Filename
		}
		return fmt.Sprintf("%s:%d:%d", filepath.ToSlash(rel), pos.Line, pos.Column)
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && repopath.SkipDir(root, path, d):
			return filepath.SkipDir
		case d.IsDir() || !isSource(path):
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Parsing only the files that could hold a call keeps a walk of
		// the whole repository fast.
		if !bytes.Contains(src, []byte("Seal(")) && !bytes.Contains(src, []byte("seal(")) {
			return nil
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		sealCalls(f, func(call *ast.CallExpr, funcs []ast.Node) {
			arg := call.Args[0]
			switch p := arg.(type) {
			case *ast.BasicLit:
				if v, err := strconv.Unquote(p.Value); err == nil && p.Kind == token.STRING {
					scan.purposes[v] = append(scan.purposes[v], at(arg.Pos()))
					return
				}
			case *ast.Ident:
				switch declaredIn(funcs, p.Name) {
				case "parameter":
					return
				case "":
					refs = append(refs, constRef{filepath.Dir(path), p.Name, at(arg.Pos())})
					return
				}
			}
			line := fmt.Sprintf(notLiteral, at(arg.Pos()), types.ExprString(arg))
			scan.unreadable = append(scan.unreadable, line)
		})
		return nil
	})
	if err != nil {
		return sealScan{}, fmt.Errorf("scan %s: %w", root, err)
	}
	consts := map[string]map[string]string{}
	for _, r := range refs {
		c, ok := consts[r.dir]
		if !ok {
			if c, err = stringConsts(fset, r.dir); err != nil {
				return sealScan{}, err
			}
			consts[r.dir] = c
		}
		if v, ok := c[r.name]; ok {
			scan.purposes[v] = append(scan.purposes[v], r.pos)
			continue
		}
		scan.unreadable = append(scan.unreadable, fmt.Sprintf(notConstant, r.pos, r.name))
	}
	return scan, nil
}

// isSource reports whether path names a Go file that is not a test.
func isSource(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

// sealCalls calls fn for each call in f of a function or method named Seal
// or seal with three arguments, with the functions that enclose the call,
// outermost first.
func sealCalls(f *ast.File, fn func(call *ast.CallExpr, funcs []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 3 || call.Ellipsis.IsValid() {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if name != "Seal" && name != "seal" {
			return true
		}
		var funcs []ast.Node
		for _, s := range stack {
			switch s.(type) {
			case *ast.FuncDecl, *ast.FuncLit:
				funcs = append(funcs, s)
			}
		}
		fn(call, funcs)
		return true
	})
}

// declaredIn reports how the functions enclosing a call declare name:
// "parameter" for a parameter of one of them, "local" for a receiver, a
// named result or a name that one of their bodies declares, and "" when
// none declares it, so it names something of the package. A body's
// declaration counts wherever it is, in scope at the call or not: a purpose
// the scan reads wrongly would hide a column, while one it cannot read only
// asks for a constant.
func declaredIn(funcs []ast.Node, name string) string {
	declared := ""
	for _, fn := range funcs {
		var typ *ast.FuncType
		var body *ast.BlockStmt
		local := false
		switch fn := fn.(type) {
		case *ast.FuncDecl:
			typ, body = fn.Type, fn.Body
			local = names(fn.Recv, name)
		case *ast.FuncLit:
			typ, body = fn.Type, fn.Body
		}
		if local || names(typ.Results, name) || declares(body, name) {
			return "local"
		}
		if names(typ.Params, name) {
			declared = "parameter"
		}
	}
	return declared
}

// names reports whether a field list declares name.
func names(fields *ast.FieldList, name string) bool {
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		for _, id := range field.Names {
			if id.Name == name {
				return true
			}
		}
	}
	return false
}

// declares reports whether body declares name: with :=, in a var or const
// declaration, or as a range variable.
func declares(body *ast.BlockStmt, name string) bool {
	found := false
	isName := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == name
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			found = found || n.Tok == token.DEFINE && slices.ContainsFunc(n.Lhs, isName)
		case *ast.ValueSpec:
			for _, id := range n.Names {
				found = found || isName(id)
			}
		case *ast.RangeStmt:
			found = found || n.Tok == token.DEFINE && (isName(n.Key) || isName(n.Value))
		}
		return !found
	})
	return found
}

// stringConsts returns the string constants that the non-test Go files of
// dir declare at package level, by name. A constant without a value repeats
// the previous one's, as Go's constant declarations do.
func stringConsts(fset *token.FileSet, dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the constants of %s: %w", dir, err)
	}
	consts := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !isSource(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			var values []ast.Expr
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if len(vs.Values) > 0 {
					values = vs.Values
				}
				for i, id := range vs.Names {
					if i >= len(values) {
						continue
					}
					lit, ok := values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if v, err := strconv.Unquote(lit.Value); err == nil {
						consts[id.Name] = v
					}
				}
			}
		}
	}
	return consts, nil
}

// checkSealedPurposes compares a scan with the purposes the modules register,
// in registration order, and with the exempt ones, each with its reason. It
// returns a line for each problem: a call whose purpose the scan could not
// read, a purpose registered twice, a purpose both registered and exempt, a
// registered or exempt purpose that nothing seals under, and a purpose that
// is sealed but neither registered nor exempt.
func checkSealedPurposes(scan sealScan, registered []string, exempt map[string]string) []string {
	problems := slices.Clone(scan.unreadable)
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	seen := map[string]bool{}
	for _, p := range registered {
		switch {
		case seen[p]:
			add("purpose %q is registered more than once", p)
		case exempt[p] != "":
			add("purpose %q is both registered and exempt", p)
		case len(scan.purposes[p]) == 0:
			add("purpose %q is registered, but nothing seals under it", p)
		}
		seen[p] = true
	}
	for _, p := range slices.Sorted(maps.Keys(exempt)) {
		if len(scan.purposes[p]) == 0 {
			add("purpose %q is exempt (%s), but nothing seals under it", p, exempt[p])
		}
	}
	for _, p := range slices.Sorted(maps.Keys(scan.purposes)) {
		if !seen[p] && exempt[p] == "" {
			add("purpose %q is sealed at %s, but no module registers its column, "+
				"so keys rotate would leave its values under a retired key",
				p, strings.Join(scan.purposes[p], ", "))
		}
	}
	return problems
}

// scanFixture scans one of the fixture trees under testdata/sealscan.
func scanFixture(t *testing.T, name string) sealScan {
	t.Helper()
	scan, err := scanSeals(filepath.Join("testdata", "sealscan", name))
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

// fixturePurposes are the purposes sealed in testdata/sealscan/clean, with
// where: under a constant of the same file, also inside a function literal,
// a constant repeated in a const block, a constant of another file of the
// package, a literal, and a literal passed to a function named seal.
var fixturePurposes = map[string][]string{
	"cipher.lower":     {"cipher/cipher.go:16:14"},
	"keys.elsewhere":   {"keys/keys.go:31:16"},
	"keys.first":       {"keys/keys.go:30:16"},
	"keys.literal":     {"keys/keys.go:32:16"},
	"keys.private_key": {"keys/keys.go:29:16", "keys/keys.go:33:30"},
}

// The scan reads purposes from literals and from constants anywhere in the
// call's package, and skips what is not a store's seal call: a wrapper's
// call whose purpose is a parameter of its own (Store.Seal and
// Keyring.Reseal pass theirs on), test files, testdata directories and
// calls of another shape.
func TestUnitScanSealsReadsTheFixture(t *testing.T) {
	scan := scanFixture(t, "clean")
	if len(scan.unreadable) != 0 {
		t.Errorf("unreadable = %q; want none", scan.unreadable)
	}
	if !maps.EqualFunc(scan.purposes, fixturePurposes, slices.Equal) {
		t.Errorf("purposes = %v\nwant %v", scan.purposes, fixturePurposes)
	}
}

// A purpose the scan cannot read fails the check at the call's position,
// whatever the argument is: a call, a package variable, an expression, a
// selector, a local variable, or a parameter that the body declares again.
func TestUnitScanSealsReportsUnreadablePurposes(t *testing.T) {
	scan := scanFixture(t, "unreadable")
	want := []string{
		fmt.Sprintf(notLiteral, "u/u.go:19:16", "purposeFor(kind)"),
		fmt.Sprintf(notLiteral, "u/u.go:21:16", `kind + ".suffix"`),
		fmt.Sprintf(notLiteral, "u/u.go:22:16", "http.MethodGet"),
		fmt.Sprintf(notLiteral, "u/u.go:24:16", "local"),
		fmt.Sprintf(notLiteral, "u/u.go:32:17", "purpose"),
		fmt.Sprintf(notConstant, "u/u.go:20:16", "purposeVar"),
	}
	if !slices.Equal(scan.unreadable, want) {
		t.Errorf("unreadable = %q\nwant %q", scan.unreadable, want)
	}
	if len(scan.purposes) != 0 {
		t.Errorf("purposes = %v; want none", scan.purposes)
	}
	if got := checkSealedPurposes(scan, nil, nil); !slices.Equal(got, want) {
		t.Errorf("check = %q\nwant %q", got, want)
	}
}

// The check passes only when the registered purposes and the exempt ones
// are exactly those sealed, each once.
func TestUnitCheckSealedPurposes(t *testing.T) {
	scan := scanFixture(t, "clean")
	all := slices.Sorted(maps.Keys(fixturePurposes))
	without := func(p string) []string {
		return slices.DeleteFunc(slices.Clone(all), func(s string) bool { return s == p })
	}
	for _, c := range []struct {
		name       string
		registered []string
		exempt     map[string]string
		want       []string
	}{
		{name: "every sealed purpose registered", registered: all},
		{
			name:       "an exempt purpose left unregistered",
			registered: without("cipher.lower"),
			exempt:     map[string]string{"cipher.lower": "sealed elsewhere"},
		},
		{
			name:       "an unregistered constant purpose",
			registered: without("keys.private_key"),
			want: []string{`purpose "keys.private_key" is sealed at keys/keys.go:29:16, ` +
				`keys/keys.go:33:30, but no module registers its column, ` +
				`so keys rotate would leave its values under a retired key`},
		},
		{
			name:       "a registered purpose that nothing seals",
			registered: append(slices.Clone(all), "keys.retired"),
			want:       []string{`purpose "keys.retired" is registered, but nothing seals under it`},
		},
		{
			name:       "a purpose registered twice",
			registered: append(slices.Clone(all), "keys.literal"),
			want:       []string{`purpose "keys.literal" is registered more than once`},
		},
		{
			name:       "an exempt purpose that nothing seals",
			registered: all,
			exempt:     map[string]string{"keys.gone": "sealed elsewhere"},
			want: []string{
				`purpose "keys.gone" is exempt (sealed elsewhere), but nothing seals under it`,
			},
		},
		{
			name:       "a purpose both registered and exempt",
			registered: all,
			exempt:     map[string]string{"keys.literal": "sealed elsewhere"},
			want:       []string{`purpose "keys.literal" is both registered and exempt`},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := checkSealedPurposes(scan, c.registered, c.exempt)
			if !slices.Equal(got, c.want) {
				t.Errorf("check = %q\nwant %q", got, c.want)
			}
		})
	}
}
