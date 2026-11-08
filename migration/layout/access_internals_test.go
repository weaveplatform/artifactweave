package layout

import (
	"cmp"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// accessInternals are the legacy packages of access that no test outside
// access may import: the evaluator and the authenticator, which ACC-11 and
// ACC-12 split into access core, and the token service, which ACC-12 moves
// there. Staged access core is judged by its path instead (accessInternal).
var accessInternals = []string{"internal/authz", "internal/auth", "internal/auth/token"}

// accessSplits are the legacy access packages whose ledger entries split
// them, so ledger.Translate names no target that places their tests in
// access.
var accessSplits = []string{"internal/auth", "internal/authz"}

// allowlistedInternals are the access internals that an allowlisted file may
// import: the authenticator and the token service, which the server root,
// the composition root and the server harnesses wire themselves, and the
// access core packages ACC-12 moves them to. The evaluator is never one.
var allowlistedInternals = []string{
	"internal/auth",
	"internal/auth/token",
	root + "/access/core/authn",
	root + "/access/core/token",
}

// accessAllowance names files that may still import allowlistedInternals,
// and the tickets whose work removes the entry.
type accessAllowance struct {
	// pattern is a path.Match pattern for one directory's files, or a
	// directory followed by /** for every file below it.
	pattern string
	// removedBy names the tickets that leave the files nothing to import.
	removedBy string
}

// accessInternalsAllowlist only shrinks: an entry that no importing file
// uses fails TestUnitTestsAvoidAccessInternals.
var accessInternalsAllowlist = []accessAllowance{
	// The server root holds the access handlers and the router: TST-04 to
	// TST-06 move its suites out, and WEB-22 deletes it.
	{"internal/server/*_test.go", "TST-04, TST-05, TST-06, WEB-22"},
	// The composition root builds the authenticator and the token service.
	{"internal/app/*_test.go", "TST-07"},
	// The server-assembly harnesses fill server.Config.Auth and Tokens until
	// WEB-22 composes the router from the access module.
	{"test/server/**", "WEB-22"},
}

// accessInternalsRule follows the failures: what a test uses instead.
const accessInternalsRule = "outside access, tests reach it through access/api and its test " +
	"support (accesstest, accessapitest, idtokentest)"

func TestUnitTestsAvoidAccessInternals(t *testing.T) {
	l, err := ledger.Load(repopath.Path(t, root+"/ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	problems := checkAccessInternals(repopath.Root(t), l, accessInternalsAllowlist)
	for _, p := range problems {
		t.Error(p)
	}
	if len(problems) != 0 {
		t.Log(accessInternalsRule)
	}
}

// checkAccessInternals returns, sorted, each import of an access internal
// by a test outside access in the repository at dir, as "file imports
// path", and each entry of allow that no importing file uses. A test is a
// _test.go file, or any Go file under test/; the walk uses repopath.SkipDir.
func checkAccessInternals(dir string, l *ledger.Ledger, allow []accessAllowance) []string {
	var problems []string
	used := make([]bool, len(allow))
	fset := token.NewFileSet()
	err := fs.WalkDir(os.DirFS(dir), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && repopath.SkipDir(dir, filepath.Join(dir, filepath.FromSlash(rel)), d):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(rel, ".go"),
			!strings.HasSuffix(rel, "_test.go") && !under(rel, "test"),
			insideAccess(l, path.Dir(rel)):
			return nil
		}
		imports, err := moduleImports(fset, dir, rel)
		if err != nil {
			problems = append(problems, err.Error())
		}
		for _, imported := range imports {
			if !accessInternal(imported) {
				continue
			}
			i := -1
			if slices.Contains(allowlistedInternals, imported) {
				i = slices.IndexFunc(allow, func(a accessAllowance) bool { return a.matches(rel) })
			}
			if i < 0 {
				problems = append(problems, rel+" imports "+imported)
				continue
			}
			used[i] = true
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}
	for i, a := range allow {
		if !used[i] {
			problems = append(problems, fmt.Sprintf(
				"allowlist entry %s (%s) matches no file that imports %s; remove it",
				a.pattern, a.removedBy, strings.Join(allowlistedInternals, ", ")))
		}
	}
	slices.Sort(problems)
	return problems
}

// insideAccess reports whether dir, a directory relative to the repository
// root, belongs to access: it is staged under access, it lies in a legacy
// package that access splits, or its ledger entry moves it into access.
func insideAccess(l *ledger.Ledger, dir string) bool {
	if under(dir, root+"/access") || underAny(dir, accessSplits...) {
		return true
	}
	if !ledger.IsLegacy(dir) {
		return false
	}
	target, _, ok := l.Translate(modulePath + "/" + dir)
	return ok && under(target, modulePath+"/"+root+"/access")
}

// accessInternal reports whether imported, a path relative to the
// repository root, is an access internal: one of accessInternals, or a
// package of staged access core other than test support, whose name ends
// in test, as idtokentest's does.
func accessInternal(imported string) bool {
	return slices.Contains(accessInternals, imported) ||
		under(imported, root+"/access/core") && !strings.HasSuffix(path.Base(imported), "test")
}

// matches reports whether the file at rel, relative to the repository root,
// is one the entry names.
func (a accessAllowance) matches(rel string) bool {
	if tree, ok := strings.CutSuffix(a.pattern, "/**"); ok {
		return under(rel, tree)
	}
	ok, err := path.Match(a.pattern, rel)
	return err == nil && ok
}

func TestUnitAccessInternalsRuleDetectsViolations(t *testing.T) {
	// file returns a Go file of package v that imports each of paths, given
	// relative to the repository root.
	file := func(paths ...string) string {
		var b strings.Builder
		b.WriteString("package v\n\nimport (\n")
		for _, p := range paths {
			fmt.Fprintf(&b, "\t_ %q\n", modulePath+"/"+p)
		}
		b.WriteString(")\n")
		return b.String()
	}
	l := &ledger.Ledger{
		Module:      modulePath,
		StagingRoot: root,
		Packages: []ledger.Package{
			{
				From:   "internal/service/identity",
				Action: ledger.ActionMove,
				To:     "access/core/identity",
			},
			{
				From:   "migration/artifacts/ecosystem/npm/http",
				Action: ledger.ActionMove,
				To:     "artifacts/ecosystem/npm/http",
			},
			{From: "internal/auth", Action: ledger.ActionSplit},
			{From: "internal/authz", Action: ledger.ActionSplit},
		},
	}
	allow := []accessAllowance{
		{"internal/server/*_test.go", "TST-04"},
		{"internal/app/*_test.go", "TST-07"},
		{"test/server/**", "WEB-22"},
	}
	evaluator, authenticator, tokens := accessInternals[0], accessInternals[1], accessInternals[2]
	conforming := map[string]string{
		"go.mod": "module " + modulePath + "\n",
		// Each allowlisted root uses its entry, with the legacy paths and
		// the access core paths ACC-12 gives them.
		"internal/server/server_test.go":  file(authenticator, tokens),
		"internal/server/handler_test.go": file(root + "/access/core/authn"),
		"internal/app/app_test.go":        file(tokens),
		"test/server/access/auth_test.go": file(authenticator),
		"test/server/support/assemble.go": file(root + "/access/core/token"),
		// Access's own tests: staged, in a split legacy package, and in a
		// legacy package whose entry moves it into access.
		root + "/access/core/authn/authn_test.go":    file(evaluator, root+"/access/core/authz"),
		"internal/auth/auth_test.go":                 file(evaluator),
		"internal/authz/authz_test.go":               file(authenticator),
		"internal/service/identity/identity_test.go": file(evaluator, tokens),
		// The published contracts and access test support, staged and legacy.
		"migration/artifacts/ecosystem/npm/http/npm_test.go": file(
			root+"/access",
			root+"/access/api",
			root+"/access/accesstest",
			root+"/access/api/accessapitest",
			root+"/access/core/idtoken/idtokentest",
			"internal/auth/idtoken/idtokentest",
		),
		// Production code is the import tiers' to judge, as are the files
		// of skipped directories.
		"migration/artifacts/ecosystem/npm/http/npm.go":             file(evaluator),
		"migration/artifacts/ecosystem/npm/http/testdata/x_test.go": file(evaluator),
		".worktrees/copy/internal/server/x/x_test.go":               file(evaluator),
		"internal/nested/go.mod":                                    "module example.com/nested\n",
		"internal/nested/n_test.go":                                 file(evaluator),
	}
	const protocol = "migration/artifacts/ecosystem/npm/http/x_test.go"

	for _, tc := range []struct {
		name  string
		file  string // where src goes; protocol when empty
		src   string // the content of file, or empty for no file
		extra string // the pattern of one more allowlist entry, or empty
		want  string // the one problem expected, or empty for none
	}{
		{name: "conforming"},
		{
			name: "protocol test importing the evaluator",
			src:  file(evaluator),
			want: protocol + " imports " + evaluator,
		},
		{
			name: "protocol test importing the authenticator",
			src:  file(authenticator),
			want: protocol + " imports " + authenticator,
		},
		{
			name: "protocol test importing the token service",
			src:  file(tokens),
			want: protocol + " imports " + tokens,
		},
		{
			name: "staged artifacts test importing access core",
			file: root + "/artifacts/core/item/item_test.go",
			src:  file(root + "/access/core/authn"),
			want: root + "/artifacts/core/item/item_test.go imports " + root + "/access/core/authn",
		},
		{
			name: "access core package whose name only starts with test",
			src:  file(root + "/access/core/testing"),
			want: protocol + " imports " + root + "/access/core/testing",
		},
		{
			name: "root test importing the evaluator",
			file: "internal/server/x_test.go",
			src:  file(evaluator),
			want: "internal/server/x_test.go imports " + evaluator,
		},
		{
			name: "harness outside test/server",
			file: "test/acceptance/steps.go",
			src:  file(authenticator),
			want: "test/acceptance/steps.go imports " + authenticator,
		},
		{
			name: "unparsable file",
			src:  "packag v\n",
			want: protocol + ":1:1: expected 'package', found packag",
		},
		{
			// The entry names npm_test.go, which imports only what is open
			// to every test.
			name:  "stale allowlist entry",
			extra: "migration/artifacts/ecosystem/npm/http/*_test.go",
			want: "allowlist entry migration/artifacts/ecosystem/npm/http/*_test.go (ECO-06) matches no file " +
				"that imports " + strings.Join(allowlistedInternals, ", ") + "; remove it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for f, content := range conforming {
				write(t, filepath.Join(dir, filepath.FromSlash(f)), content)
			}
			if tc.src != "" {
				f := cmp.Or(tc.file, protocol)
				write(t, filepath.Join(dir, filepath.FromSlash(f)), tc.src)
			}
			entries := allow
			if tc.extra != "" {
				entries = append(slices.Clone(allow), accessAllowance{tc.extra, "ECO-06"})
			}
			problems := checkAccessInternals(dir, l, entries)
			if tc.want == "" && problems != nil {
				t.Fatalf("problems in a conforming tree: %q", problems)
			}
			if tc.want != "" && !slices.Equal(problems, []string{tc.want}) {
				t.Fatalf("problems = %q; want %q", problems, tc.want)
			}
		})
	}

	t.Run("missing root", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		problems := checkAccessInternals(missing, l, nil)
		if len(problems) != 1 {
			t.Errorf("problems = %q; want one for the missing root", problems)
		}
	})
}
