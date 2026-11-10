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

// withPrincipal is the kernel function that stores a principal in a context
// without a permission decider.
const withPrincipal = "WithPrincipal"

// withPrincipalSites are the production files that may name WithPrincipal.
// Everywhere else an authorised principal is stored together with its decider,
// through accessapi.WithCaller or authn.WithAuthorized, which forwards to it. A
// principal stored alone still compiles and reads back, but it has no decider:
// the Docker registry refuses it, pkgroute and terraformbackend deny it, and
// itemkit.AccessFor quietly evaluates its permissions again for every
// question.
var withPrincipalSites = []string{
	// The bridge's alias of kernel.WithPrincipal, which PRO-02 deletes.
	"internal/auth/bridge_kernel.go",
	// WithCaller, which binds the decider straight after.
	root + "/access/api/context.go",
	// The remote reindex stores the caller it has just rechecked without a
	// decider, so that the repository check that follows evaluates that
	// caller's current permissions rather than the request's.
	root + "/artifacts/adapters/http/itemkit/reindex.go",
}

// withPrincipalRule ends every problem: what to use instead.
const withPrincipalRule = "a principal stored without its decider has no permissions; " +
	"store an authorised one with accessapi.WithCaller or authn.WithAuthorized"

func TestUnitWithPrincipalCallers(t *testing.T) {
	for _, p := range checkWithPrincipalCallers(repopath.Root(t)) {
		t.Error(p)
	}

	// file returns a Go file of package v whose function v runs body, which
	// starts on line 11.
	file := func(body string) string {
		return `package v

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

func v(ctx context.Context, p kernel.Principal) {
` + body + "\n}\n"
	}
	// uses names WithPrincipal every way, placed where the walk must allow it
	// or must not look.
	uses := file(`	_ = kernel.WithPrincipal(ctx, p)
	_ = auth.WithPrincipal(ctx, p)
	f := WithPrincipal
	_ = f`)
	conforming := map[string]string{
		"go.mod":                          "module github.com/weaveplatform/artifactweave\n",
		"internal/auth/bridge_kernel.go":  uses,
		"migration/access/api/context.go": uses,
		"migration/artifacts/adapters/http/itemkit/reindex.go": uses,
		"internal/web/handler_test.go":                         uses,
		"migration/kernel/context_test.go":                     uses,
		"internal/web/testdata/x.go":                           uses,
		"internal/nested/go.mod":                               "module example.com/nested\n",
		"internal/nested/n.go":                                 uses,
		".worktrees/copy/internal/web/x/x.go":                  uses,
		// The declaration itself is no use, nor is a comment that names it.
		"migration/kernel/context.go": `package kernel

import "context"

// WithPrincipal(ctx, p) stores p.
func WithPrincipal(ctx context.Context, p any) context.Context { return ctx }
`,
	}
	const v = "internal/web/v/v.go"

	for _, tc := range []struct {
		name string
		file string // where src goes; v when empty
		src  string // the content of file, or empty for no file
		want string // a part of the one problem expected, or empty for none
	}{
		{name: "conforming"},
		{
			name: "call through the bridge",
			src:  file(`	_ = auth.WithPrincipal(ctx, p)`),
			want: v + ":11: names WithPrincipal; " + withPrincipalRule,
		},
		{
			name: "call through the kernel",
			src:  file(`	_ = kernel.WithPrincipal(ctx, p)`),
			want: v + ":11: names WithPrincipal",
		},
		{
			name: "call through an import name",
			src: strings.Replace(
				file(`	_ = principals.WithPrincipal(ctx, p)`),
				`	"github.com/weaveplatform/artifactweave/migration/kernel"`,
				`	principals "github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/kernel"`,
				1,
			),
			want: v + ":12: names WithPrincipal",
		},
		{
			name: "function value",
			src: file(`	store := kernel.WithPrincipal
	_ = store(ctx, p)`),
			want: v + ":11: names WithPrincipal",
		},
		{
			name: "unqualified call in internal/auth",
			file: "internal/auth/x.go",
			src:  file(`	_ = WithPrincipal(ctx, p)`),
			want: "internal/auth/x.go:11: names WithPrincipal",
		},
		{
			name: "unqualified call elsewhere in the kernel",
			file: "migration/kernel/other.go",
			src:  file(`	_ = WithPrincipal(ctx, p)`),
			want: "migration/kernel/other.go:11: names WithPrincipal",
		},
		{
			name: "an alias of it",
			file: "internal/web/bridge.go",
			src:  "package web\n\nimport \"github.com/weaveplatform/artifactweave/migration/kernel\"\n\nvar WithPrincipal = kernel.WithPrincipal\n",
			want: "internal/web/bridge.go:5: names WithPrincipal",
		},
		{
			name: "unparsable file",
			src:  "packag v // WithPrincipal\n",
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
			problems := checkWithPrincipalCallers(dir)
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
		if problems := checkWithPrincipalCallers(missing); len(problems) != 1 {
			t.Errorf("problems = %q; want one for the missing root", problems)
		}
	})
}

// checkWithPrincipalCallers returns, sorted, each place where a production Go
// file of the repository at dir names WithPrincipal outside
// withPrincipalSites, walking with repopath.SkipDir. Test files are left
// alone: they store bare principals on purpose, to check what a handler does
// with one.
func checkWithPrincipalCallers(dir string) []string {
	var problems []string
	fset := token.NewFileSet()
	err := fs.WalkDir(os.DirFS(dir), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && repopath.SkipDir(dir, filepath.Join(dir, filepath.FromSlash(rel)), d):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go"),
			slices.Contains(withPrincipalSites, rel):
			return nil
		}
		found, err := withPrincipalUses(fset, dir, rel)
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

// withPrincipalUses returns each place where the Go file at rel, a path
// relative to dir, names WithPrincipal: a call or a function value, qualified
// or not. A function declared with that name, or a variable or constant
// declared with it, is no use; whatever the declaration names on its right is.
func withPrincipalUses(fset *token.FileSet, dir, rel string) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	// Almost no file spells the name, so the walk parses only a handful.
	if !bytes.Contains(src, []byte(withPrincipal)) {
		return nil, nil
	}
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var found []string
	// declared holds the identifiers that declare the name, and the selected
	// names already reported with their selector. ast.Inspect visits a node
	// before its children, so each is recorded before its identifier is seen.
	declared := map[*ast.Ident]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		var id *ast.Ident
		switch n := n.(type) {
		case *ast.FuncDecl:
			declared[n.Name] = true
		case *ast.ValueSpec:
			for _, name := range n.Names {
				declared[name] = true
			}
		case *ast.SelectorExpr:
			declared[n.Sel] = true
			id = n.Sel
		case *ast.Ident:
			if !declared[n] {
				id = n
			}
		}
		if id != nil && id.Name == withPrincipal {
			pos := fset.Position(id.Pos())
			found = append(found, fmt.Sprintf(
				"%s:%d: names %s; %s", pos.Filename, pos.Line, withPrincipal, withPrincipalRule))
		}
		return true
	})
	return found, nil
}
