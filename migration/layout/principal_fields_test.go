package layout

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// principalInternals are the Principal fields that access sets when it
// verifies a credential and trusts when it rechecks one for a long request or
// queued work. The kernel declares Principal, so they are exported, but a
// value set outside access could pass that recheck for a credential nobody
// verified.
var principalInternals = []string{"IsTokenAuthenticated", "PasswordRevision", "TokenSubject"}

// sharedInternal is the one of them that CredentialReference and
// DownloadCredential declare too, for the revision they persist. Setting it in
// their literals is how other packages build references, so only a Principal
// literal may not set it.
const sharedInternal = "PasswordRevision"

// principalPackages are the packages whose Principal a qualified literal type
// names: the kernel and the bridge to it that internal/auth keeps until the
// bridges go.
var principalPackages = []string{modulePath + "/internal/auth", modulePath + "/" + root + "/kernel"}

// internalsRule ends every problem: what is allowed, and where the reason is.
const internalsRule = "only access and the kernel may read or set it (kernel.Principal)"

func TestUnitPrincipalInternals(t *testing.T) {
	for _, p := range checkPrincipalInternals(repopath.Root(t)) {
		t.Error(p)
	}

	// file returns a Go file of package v whose function v runs body, which
	// starts on line 11.
	file := func(body string) string {
		return `package v

import (
	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

type Principal struct{ PasswordRevision [32]byte }

func v(p auth.Principal, ref kernel.CredentialReference, r [32]byte) {
` + body + "\n}\n"
	}
	// uses breaks every rule, placed where the walk must allow it or must not
	// look.
	uses := file(`	_, _, _ = p.IsTokenAuthenticated, p.TokenSubject, ref.PasswordRevision
	_ = auth.Principal{IsTokenAuthenticated: true, TokenSubject: "s", PasswordRevision: r}`)
	conforming := map[string]string{
		"go.mod":                           "module github.com/weaveplatform/artifactweave\n",
		"migration/kernel/principal.go":    uses,
		"migration/kernel/sub/sub_test.go": uses,
		"migration/access/core/authn/a.go": uses,
		"internal/auth/testdata/x.go":      uses,
		"internal/nested/go.mod":           "module example.com/nested\n",
		"internal/nested/n.go":             uses,
		".worktrees/copy/internal/x/x.go":  uses,
		"internal/server/unrelated/u.go":   "package u\n",
		"migration/artifacts/ecosystem/huggingface/http/xet.go": file(
			`	_ = []auth.DownloadCredential{{Kind: "password", PasswordRevision: r}}`,
		),
		"internal/server/queue/queue_test.go": file(
			`	_ = map[string]kernel.CredentialReference{"a": {PasswordRevision: r}}`,
		),
	}
	const v = "internal/server/v/v.go"

	for _, tc := range []struct {
		name string
		file string // where src goes; v when empty
		src  string // the content of file, or empty for no file
		want string // a part of the one problem expected, or empty for none
	}{
		{name: "conforming"},
		{
			name: "selector IsTokenAuthenticated",
			src:  file(`	_ = p.IsTokenAuthenticated`),
			want: v + ":11: selects IsTokenAuthenticated; " + internalsRule,
		},
		{
			name: "selector TokenSubject",
			src:  file(`	_ = p.TokenSubject`),
			want: v + ":11: selects TokenSubject",
		},
		{
			name: "selector PasswordRevision on a reference",
			src:  file(`	_ = ref.PasswordRevision`),
			want: v + ":11: selects PasswordRevision",
		},
		{
			name: "key IsTokenAuthenticated",
			src:  file(`	_ = auth.Principal{Name: "ann", IsTokenAuthenticated: true}`),
			want: v + ":11: sets IsTokenAuthenticated",
		},
		{
			name: "key TokenSubject in any literal",
			src:  file(`	_ = struct{ TokenSubject string }{TokenSubject: "jfac@x/users/ann"}`),
			want: v + ":11: sets TokenSubject",
		},
		{
			name: "key PasswordRevision in auth.Principal",
			src:  file(`	_ = auth.Principal{PasswordRevision: r}`),
			want: v + ":11: sets PasswordRevision in a Principal literal",
		},
		{
			name: "key PasswordRevision in kernel.Principal",
			src:  file(`	_ = &kernel.Principal{PasswordRevision: r}`),
			want: v + ":11: sets PasswordRevision in a Principal literal",
		},
		{
			name: "key PasswordRevision in Principal",
			src:  file(`	_ = Principal{PasswordRevision: r}`),
			want: v + ":11: sets PasswordRevision in a Principal literal",
		},
		{
			name: "key PasswordRevision through an import name",
			src: strings.Replace(
				file(`	_ = principals.Principal{PasswordRevision: r}`),
				`	"github.com/weaveplatform/artifactweave/migration/kernel"`,
				`	principals "github.com/weaveplatform/artifactweave/migration/kernel"`,
				1,
			),
			want: v + ":11: sets PasswordRevision in a Principal literal",
		},
		{
			name: "key PasswordRevision in an elided slice element",
			src:  file(`	_ = []auth.Principal{{Name: "ann"}, {PasswordRevision: r}}`),
			want: v + ":11: sets PasswordRevision in a Principal literal",
		},
		{
			name: "key PasswordRevision in an elided map value",
			src: file(`	_ = map[string][]*kernel.Principal{
		"ann": {{PasswordRevision: r}},
	}`),
			want: v + ":12: sets PasswordRevision in a Principal literal",
		},
		{
			name: "file in internal/auth, which holds only bridges",
			file: "internal/auth/principal.go",
			src:  file(`	_ = p.TokenSubject`),
			want: "internal/auth/principal.go:11: selects TokenSubject",
		},
		{
			name: "subpackage of internal/auth",
			file: "internal/auth/token/token.go",
			src:  file(`	_ = p.TokenSubject`),
			want: "internal/auth/token/token.go:11: selects TokenSubject",
		},
		{
			name: "unparsable file",
			src:  "packag v // TokenSubject\n",
			want: v + ":1:1: expected 'package'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for f, content := range conforming {
				write(t, filepath.Join(dir, filepath.FromSlash(f)), content)
			}
			if tc.src != "" {
				f := cmp.Or(tc.file, v)
				write(t, filepath.Join(dir, filepath.FromSlash(f)), tc.src)
			}
			problems := checkPrincipalInternals(dir)
			if tc.want == "" && problems != nil {
				t.Fatalf("problems in a conforming tree: %q", problems)
			}
			if tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.want)) {
				t.Fatalf("problems = %q; want one containing %q", problems, tc.want)
			}
		})
	}

	t.Run("missing root", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		if problems := checkPrincipalInternals(missing); len(problems) != 1 {
			t.Errorf("problems = %q; want one for the missing root", problems)
		}
	})
}

// checkPrincipalInternals returns, sorted, each use of a principal internal by
// a Go file of the repository at dir outside access and the kernel, walking
// with repopath.SkipDir.
func checkPrincipalInternals(dir string) []string {
	var problems []string
	fset := token.NewFileSet()
	err := fs.WalkDir(os.DirFS(dir), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && repopath.SkipDir(dir, filepath.Join(dir, filepath.FromSlash(rel)), d):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(rel, ".go") || ownsPrincipalInternals(rel):
			return nil
		}
		found, err := principalInternalUses(fset, dir, rel)
		if err != nil {
			problems = append(problems, err.Error())
		}
		problems = append(problems, found...)
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}
	slices.Sort(problems)
	return problems
}

// ownsPrincipalInternals reports whether the Go file at rel, a path relative
// to the repository root, belongs to access or the kernel: any file under
// the staged kernel and access trees. The legacy internal/auth holds only
// bridges, which name none of the fields.
func ownsPrincipalInternals(rel string) bool {
	return strings.HasPrefix(rel, root+"/kernel/") || strings.HasPrefix(rel, root+"/access/")
}

// principalInternalUses returns each use of a principal internal in the Go
// file at rel, a path relative to dir: a selector of any of them, a key of
// IsTokenAuthenticated or TokenSubject in any composite literal, and a key of
// PasswordRevision in a Principal literal.
func principalInternalUses(fset *token.FileSet, dir, rel string) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	// A file that never spells a field name cannot use it, and almost no file
	// does, so the walk parses only a handful of the module's files.
	if !slices.ContainsFunc(principalInternals, func(name string) bool {
		return bytes.Contains(src, []byte(name))
	}) {
		return nil, nil
	}
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	imports := importNames(f)
	var found []string
	report := func(id *ast.Ident, format string, args ...any) {
		pos := fset.Position(id.Pos())
		found = append(found, fmt.Sprintf(
			"%s:%d: %s; %s", pos.Filename, pos.Line, fmt.Sprintf(format, args...), internalsRule))
	}
	// elided holds the type that a composite literal written without one takes
	// from the array, slice or map literal that holds it. ast.Inspect visits a
	// literal before its elements, so the type is known when they are visited.
	elided := map[*ast.CompositeLit]ast.Expr{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if slices.Contains(principalInternals, n.Sel.Name) {
				report(n.Sel, "selects %s", n.Sel.Name)
			}
		case *ast.CompositeLit:
			typ := n.Type
			if typ == nil {
				typ = elided[n]
			}
			elem, key := elementTypes(typ)
			for _, e := range n.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					inherit(elided, e, elem)
					continue
				}
				inherit(elided, kv.Key, key)
				inherit(elided, kv.Value, elem)
				switch id, ok := kv.Key.(*ast.Ident); {
				case !ok || !slices.Contains(principalInternals, id.Name):
				case id.Name != sharedInternal:
					report(id, "sets %s", id.Name)
				case isPrincipal(typ, imports):
					report(id, "sets %s in a Principal literal", id.Name)
				}
			}
		}
		return true
	})
	return found, nil
}

// elementTypes returns the element (or map value) type and the map key type
// of a literal of type typ, without a pointer's star: an element of type *T
// written without its type is written as T's literal.
func elementTypes(typ ast.Expr) (elem, key ast.Expr) {
	deref := func(e ast.Expr) ast.Expr {
		if star, ok := e.(*ast.StarExpr); ok {
			return star.X
		}
		return e
	}
	switch t := typ.(type) {
	case *ast.ArrayType:
		return deref(t.Elt), nil
	case *ast.MapType:
		return deref(t.Value), deref(t.Key)
	}
	return nil, nil
}

// inherit records typ as the type of e when e is a composite literal written
// without one.
func inherit(elided map[*ast.CompositeLit]ast.Expr, e, typ ast.Expr) {
	if lit, ok := e.(*ast.CompositeLit); ok && lit.Type == nil && typ != nil {
		elided[lit] = typ
	}
}

// isPrincipal reports whether typ names the request principal: Principal
// itself, or Principal of a package in principalPackages under whatever name
// the file imports it.
func isPrincipal(typ ast.Expr, imports map[string]string) bool {
	switch t := typ.(type) {
	case *ast.Ident:
		return t.Name == "Principal"
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && t.Sel.Name == "Principal" &&
			slices.Contains(principalPackages, imports[x.Name])
	}
	return false
}
