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

// allowlistedInternals are the access internals that an allowlisted file may
// import: the authenticator and the token service, which the composition
// root and the server harnesses wire into the router themselves, and the
// roles and OIDC services, with which the router's route and principal
// matrix creates its principals' environments, roles, providers and
// mappings. The evaluator is never one, and neither is a legacy path of
// them.
var allowlistedInternals = []string{
	root + "/access/core/authn",
	root + "/access/core/token",
	root + "/access/core/roles",
	root + "/access/core/oidc",
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
	// The composition root builds the authenticator and the token service,
	// and its own tests set the password hash cost, check the seeded
	// principals and issue a token with them; the router's guards beside it
	// fill RouterConfig.Auth and Tokens and create the matrix's roles and
	// OIDC providers. No ticket removes these.
	{"migration/app/*_test.go", "none: app's own tests"},
	// The server suites and their harnesses fill app.RouterConfig.Auth and
	// Tokens, the fields through which the router takes them.
	{"test/server/**", "none: RouterConfig takes Auth and Tokens"},
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
// root, belongs to access: it is staged under access, or its ledger entry
// moves it into access.
func insideAccess(l *ledger.Ledger, dir string) bool {
	if under(dir, root+"/access") {
		return true
	}
	if !ledger.IsLegacy(dir) {
		return false
	}
	target, _, ok := l.Translate(modulePath + "/" + dir)
	return ok && under(target, modulePath+"/"+root+"/access")
}

// accessInternal reports whether imported, a path relative to the
// repository root, is an access internal: a package of staged access core
// other than test support, whose name ends in test, as idtokentest's does.
func accessInternal(imported string) bool {
	return under(imported, root+"/access/core") && !strings.HasSuffix(path.Base(imported), "test")
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
	// internal/web is made up, outside every ledger entry, so that no move
	// rewrites this data.
	l := &ledger.Ledger{
		Module:      modulePath,
		StagingRoot: root,
		Packages: []ledger.Package{
			{
				From:   "internal/service/accounts",
				Action: ledger.ActionMove,
				To:     "access/core/accounts",
			},
			{
				From:   "internal/web/xyz",
				Action: ledger.ActionMove,
				To:     "artifacts/ecosystem/npm/http",
			},
			{From: "internal/web/auth", Action: ledger.ActionMove, To: "access/core/authn"},
		},
	}
	allow := []accessAllowance{
		{"internal/web/*_test.go", "TST-04"},
		{"migration/app/*_test.go", "TST-07"},
		{"test/server/**", "WEB-22"},
	}
	evaluator := root + "/access/core/authz"
	authenticator, tokens := allowlistedInternals[0], allowlistedInternals[1]
	conforming := map[string]string{
		"go.mod": "module " + modulePath + "\n",
		// Each allowlisted root uses its entry; a file under test/ is a
		// test even without the _test.go suffix.
		"internal/web/server_test.go":     file(authenticator, tokens),
		"migration/app/run_test.go":       file(tokens),
		"test/server/access/auth_test.go": file(authenticator),
		"test/server/support/assemble.go": file(tokens),
		// Access's own tests: staged, and in legacy packages whose entries
		// move them into access.
		root + "/access/core/authn/authn_test.go":    file(evaluator, authenticator),
		"internal/web/auth/auth_test.go":             file(evaluator, authenticator),
		"internal/service/accounts/accounts_test.go": file(evaluator, tokens),
		// The published contracts and access test support, staged and legacy.
		"internal/web/xyz/npm_test.go": file(
			root+"/access",
			root+"/access/api",
			root+"/access/accesstest",
			root+"/access/api/accessapitest",
			root+"/access/core/idtoken/idtokentest",
			"migration/access/core/idtoken/idtokentest",
		),
		// Production code is the import tiers' to judge, as are the files
		// of skipped directories.
		"internal/web/xyz/npm.go":                  file(evaluator),
		"internal/web/xyz/testdata/x_test.go":      file(evaluator),
		".worktrees/copy/internal/web/x/x_test.go": file(evaluator),
		"internal/nested/go.mod":                   "module example.com/nested\n",
		"internal/nested/n_test.go":                file(evaluator),
	}
	const protocol = "internal/web/xyz/x_test.go"

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
			file: "internal/web/x_test.go",
			src:  file(evaluator),
			want: "internal/web/x_test.go imports " + evaluator,
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
			extra: "internal/web/xyz/*_test.go",
			want: "allowlist entry internal/web/xyz/*_test.go (ECO-06) matches no file " +
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
