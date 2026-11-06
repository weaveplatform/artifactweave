package layout

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/devtools/ledger"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// root is the staging root. The rows name staged packages relative to it, so
// promotion changes it to "internal" and leaves the rows as they are.
const root = "migration"

// modulePath is the module whose imports the rows judge. ledger.Validate pins
// the ledger's module to the same path.
const modulePath = "github.com/weaveplatform/artifactweave"

// modules and otherTops are the only names allowed directly under root
// (decision 0044). devtools/ledger holds the same set to validate ledger
// targets, so the two change together.
var (
	modules = []string{
		"artifacts", "access", "configuration", "notifications", "retention", "federation",
		"releases", "workers", "evidence", "governance", "security", "operations", "estate",
	}
	otherTops = []string{"kernel", "platform", "gateway", "app", "layout", "devtools"}
)

// walkRoots are the trees whose imports the rows judge: the staged packages and
// the two pkg/ packages that keep their paths. Legacy packages are judged once
// they move.
var walkRoots = []string{root, "pkg/client", "pkg/containertest"}

// sharedTypes are the staged packages that generated clients and servers name
// through x-go-type-import (decision 0002), so rows 3 and 6 allow them.
var sharedTypes = []string{"artifacts/api/repo", "kernel/wire"}

func TestUnitImportTiers(t *testing.T) {
	l, err := ledger.Load(repopath.Path(t, root+"/ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	violations, splits := checkImports(repopath.Root(t), l)
	for _, s := range splits {
		t.Log(s)
	}
	t.Logf("%d imports of split legacy packages allowed", len(splits))
	for _, v := range violations {
		t.Error(v)
	}
}

// checkImports judges every import of a module package by a Go file under
// walkRoots in the repository at dir, walking with repopath.SkipDir. It returns,
// sorted, the violations and the allowed imports of split legacy packages.
func checkImports(dir string, l *ledger.Ledger) (violations, splits []string) {
	fset := token.NewFileSet()
	visit := func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && repopath.SkipDir(dir, filepath.Join(dir, filepath.FromSlash(rel)), d):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(rel, ".go"):
			return nil
		}
		imports, err := moduleImports(fset, dir, rel)
		if err != nil {
			violations = append(violations, err.Error())
		}
		for _, imported := range imports {
			switch line, split := judge(l, rel, imported); {
			case split:
				splits = append(splits, line)
			case line != "":
				violations = append(violations, line)
			}
		}
		return nil
	}
	for _, walkRoot := range walkRoots {
		if err := fs.WalkDir(os.DirFS(dir), walkRoot, visit); err != nil {
			violations = append(violations, err.Error())
		}
	}
	slices.Sort(violations)
	slices.Sort(splits)
	return violations, splits
}

// moduleImports returns the imports of the Go file at rel, a path relative to
// dir, that name packages of the module, as paths relative to the repository
// root.
func moduleImports(fset *token.FileSet, dir, rel string) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(fset, rel, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var imports []string
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		if p, ok := strings.CutPrefix(p, modulePath+"/"); ok {
			imports = append(imports, p)
		}
	}
	return imports, nil
}

// judge applies the rows to the import of imported, a path relative to the
// repository root, by the Go file at file. It returns the line that reports the
// import, with split true when the line logs an allowed import of a split legacy
// package. line is empty when the import is allowed without a log.
func judge(l *ledger.Ledger, file, imported string) (line string, split bool) {
	from, fromStaged := rulePath(path.Dir(file))
	to, toStaged := rulePath(imported)
	shown := imported
	if !toStaged && ledger.IsLegacy(imported) {
		target, action, ok := l.Translate(modulePath + "/" + imported)
		switch {
		case !ok:
			return file + " imports " + imported + " (legacy package with no ledger entry)", false
		case action == ledger.ActionDelete:
			return file + " imports " + imported + " (legacy package marked delete)", false
		case action == ledger.ActionSplit && fromStaged:
			return file + " imports " + imported +
				" (legacy package marked split; allowed until it is emptied)", true
		case action == ledger.ActionSplit:
			// Decision 0044 lets staged code import a split package until its
			// narrowing. pkg/client and pkg/containertest are not staged code,
			// so the rows judge the legacy path itself, and rows 3 and 4
			// reject it.
		default:
			target = strings.TrimPrefix(target, modulePath+"/")
			to, _ = rulePath(target)
			shown = imported + ", judged as " + target
		}
	}
	row, ok := allowed(from, to, strings.HasSuffix(file, "_test.go"))
	if ok {
		return "", false
	}
	return fmt.Sprintf("%s imports %s (row %d: %s)", file, shown, row, rows[row].text), false
}

// rulePath returns the path by which the rows name the package at p, a path
// relative to the repository root, and whether the package is staged. A staged
// package is named relative to root ("artifacts/core/item"), any other package
// relative to the repository root ("pkg/client/artifactory", "test/server",
// "api"). The two cannot be confused: TestUnitTopLevelDirectories keeps every
// staged path starting with a name in modules or otherTops, and no directory
// at the repository root has such a name.
func rulePath(p string) (string, bool) {
	if rest, ok := strings.CutPrefix(p, root+"/"); ok {
		return rest, true
	}
	return p, false
}

// rule is a row of the dependency table. decide reports whether the row applies
// to an import of to by a file of from, test telling whether that file is a
// _test.go file, and when it applies, whether it allows the import.
type rule struct {
	text   string
	decide func(from, to string, test bool) (applies, ok bool)
}

// rows is the dependency table, numbered by index; the first row that applies
// decides. Tickets cite rows by number, so a change extends a row in place
// instead of inserting one.
var rows = []rule{
	{
		// Without it, row 10 would reject the external tests of a module root.
		text:   "an external test package may import its own directory",
		decide: func(from, to string, _ bool) (bool, bool) { return from == to, true },
	},
	{
		text:   "a test file may import test support",
		decide: func(_, to string, test bool) (bool, bool) { return test && testSupport(to), true },
	},
	{
		text:   "only a test file may import test/",
		decide: func(_, to string, test bool) (bool, bool) { return !test && under(to, "test"), false },
	},
	{
		text: "pkg/client imports only pkg/client, kernel/wire and artifacts/api/repo",
		decide: func(from, to string, _ bool) (bool, bool) {
			ok := under(to, "pkg/client") || slices.Contains(sharedTypes, to)
			return under(from, "pkg/client"), ok
		},
	},
	{
		text: "pkg/containertest imports no staged or legacy package",
		decide: func(from, to string, _ bool) (bool, bool) {
			return under(from, "pkg/containertest") && (staged(to) || ledger.IsLegacy(to)), false
		},
	},
	{
		text: "app, devtools and layout may import anything",
		decide: func(from, _ string, _ bool) (bool, bool) {
			return underAny(from, "app", "devtools", "layout"), true
		},
	},
	{
		// A named exception to row 7.
		text: "platform/gen may import artifacts/api/repo and kernel/wire",
		decide: func(from, to string, _ bool) (bool, bool) {
			return under(from, "platform/gen") && slices.Contains(sharedTypes, to), true
		},
	},
	{
		text: "platform and kernel import no module, gateway, app, devtools or layout",
		decide: func(from, to string, _ bool) (bool, bool) {
			barred := moduleOf(to) != "" || underAny(to, "gateway", "app", "devtools", "layout")
			return underAny(from, "platform", "kernel") && barred, false
		},
	},
	{
		text: "gateway imports only module api packages, kernel, platform/httpkit and gateway",
		decide: func(from, to string, _ bool) (bool, bool) {
			ok := isAPI(to) || underAny(to, "kernel", "platform/httpkit", "gateway")
			return under(from, "gateway"), ok
		},
	},
	{
		text: "a module's api imports only kernel, platform/outbox and its own api",
		decide: func(from, to string, _ bool) (bool, bool) {
			ok := under(to, "kernel") || to == "platform/outbox" || under(to, moduleOf(from)+"/api")
			return isAPI(from), ok
		},
	},
	{
		text: "no module package imports a module root; only app wires modules",
		decide: func(from, to string, _ bool) (bool, bool) {
			return moduleOf(from) != "" && slices.Contains(modules, to), false
		},
	},
	{
		text: "a module imports another module only through its api",
		decide: func(from, to string, _ bool) (bool, bool) {
			m, other := moduleOf(from), moduleOf(to)
			return m != "" && other != "" && other != m, isAPI(to)
		},
	},
	{
		text: "a module's core never imports its own adapters",
		decide: func(from, to string, _ bool) (bool, bool) {
			m := moduleOf(from)
			return m != "" && under(from, m+"/core") && under(to, m+"/adapters"), false
		},
	},
	{
		// Test files are left alone because tests compose slices: nimmodel's
		// pairing test mounts the docker protocol package.
		text: "only its own subtree and artifacts/adapters/http import an ecosystem http package",
		decide: func(from, to string, test bool) (bool, bool) {
			h := ecosystemHTTP(to)
			ok := under(from, h) || under(from, "artifacts/adapters/http")
			return moduleOf(from) != "" && !test && h != "", ok
		},
	},
	{
		text: "a module may import its own packages, platform, kernel and pkg",
		decide: func(from, to string, _ bool) (bool, bool) {
			m := moduleOf(from)
			return m != "" && (under(to, m) || underAny(to, "platform", "kernel", "pkg")), true
		},
	},
	{
		text: "a module's test files may import api",
		decide: func(from, to string, test bool) (bool, bool) {
			return moduleOf(from) != "" && test && to == "api", true
		},
	},
	{
		text:   "a module imports nothing else",
		decide: func(from, _ string, _ bool) (bool, bool) { return moduleOf(from) != "", false },
	},
}

// allowed returns the first row that applies to an import of to by a file of
// from, testFile telling whether that file is a _test.go file, and whether the
// row allows the import. When no row applies, row is -1 and the import is
// allowed: platform and kernel may import whatever rows 2 and 7 leave them, and
// pkg/containertest whatever rows 2 and 4 leave it.
func allowed(from, to string, testFile bool) (row int, ok bool) {
	for i, r := range rows {
		if applies, ok := r.decide(from, to, testFile); applies {
			return i, ok
		}
	}
	return -1, true
}

// testSupport reports whether p is test support, which row 1 lets any test file
// import. FND-05 also names */adapters/storetest and pkg/containertest; the
// suffix rule covers both.
func testSupport(p string) bool {
	inmem, _ := path.Match("*/adapters/inmem", p)
	return inmem || under(p, "platform/testkit") ||
		p == "artifacts/adapters/upstream/fakeupstream" ||
		strings.HasSuffix(path.Base(p), "test") ||
		under(p, "test")
}

// staged reports whether p, a rule path, names a staged package.
func staged(p string) bool {
	top, _, _ := strings.Cut(p, "/")
	return slices.Contains(modules, top) || slices.Contains(otherTops, top)
}

// moduleOf returns the module that p, a rule path, lies in, or "" for none.
func moduleOf(p string) string {
	top, _, _ := strings.Cut(p, "/")
	if slices.Contains(modules, top) {
		return top
	}
	return ""
}

// isAPI reports whether p is a module's api package or lies below one.
func isAPI(p string) bool {
	m := moduleOf(p)
	return m != "" && under(p, m+"/api")
}

// ecosystemHTTP returns the artifacts/ecosystem/<type>/http directory that p
// lies in, or "" for none.
func ecosystemHTTP(p string) string {
	e := strings.Split(p, "/")
	if len(e) >= 4 && e[0] == "artifacts" && e[1] == "ecosystem" && e[3] == "http" {
		return strings.Join(e[:4], "/")
	}
	return ""
}

// under reports whether p is dir or lies below it.
func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// underAny reports whether p is one of dirs or lies below one.
func underAny(p string, dirs ...string) bool {
	return slices.ContainsFunc(dirs, func(dir string) bool { return under(p, dir) })
}

func TestUnitImportRulesDetectViolations(t *testing.T) {
	const (
		code, test       = false, true // whether the importing file is a _test.go file
		violation, allow = false, true
		none             = -1 // no row applies
	)
	// Each row has an allow case and a violation case. A case that another
	// row decides shows where the row under test stops applying.
	cases := []struct {
		row      int    // the row the case exercises
		from, to string // rule paths
		test     bool
		by       int // the row that decides the import
		ok       bool
	}{
		{0, "artifacts", "artifacts", test, 0, allow},
		{0, "artifacts/core/item", "artifacts", test, 10, violation},

		{1, "access/core/user", "artifacts/adapters/inmem", test, 1, allow},
		{1, "access/core/user", "app/adapters/inmem", test, 1, allow},
		{1, "access/core/user", "artifacts/adapters/storetest", test, 1, allow},
		{1, "gateway/rest", "platform/testkit/fault", test, 1, allow},
		{1, "access/core/user", "artifacts/adapters/upstream/fakeupstream", test, 1, allow},
		{1, "access/api", "artifacts/core/formattest", test, 1, allow},
		{1, "kernel", "test/server/support", test, 1, allow},
		{1, "access/api", "pkg/containertest", test, 1, allow},
		{1, "access/core/user", "artifacts/adapters/inmem", code, 11, violation},
		{1, "access/core/user", "artifacts/core/item", test, 11, violation},
		{1, "access/core/user", "artifacts/adapters/inmem/sub", test, 11, violation},

		{2, "app", "test/server/support", code, 2, violation},
		{2, "app", "test/server/support", test, 1, allow},

		{3, "pkg/client/artifactory", "kernel/wire", code, 3, allow},
		{3, "pkg/client/artifactory", "artifacts/api/repo", code, 3, allow},
		{3, "pkg/client/artifactory", "pkg/client/access", code, 3, allow},
		{3, "pkg/client/artifactory", "kernel", code, 3, violation},
		{3, "pkg/client/artifactory", "api", code, 3, violation},
		{3, "pkg/client/artifactory", "pkg/types", code, 3, violation},

		{4, "pkg/containertest", "platform/blob", code, 4, violation},
		{4, "pkg/containertest", "pkg/store", code, 4, violation},
		{4, "pkg/containertest", "pkg/client/artifactory", code, none, allow},

		{5, "app", "artifacts/adapters/sql", code, 5, allow},
		{5, "devtools/stubgen", "access/core/token", code, 5, allow},
		{5, "layout", "gateway/rest", code, 5, allow},
		{5, "artifacts/app", "access/core/token", code, 11, violation},

		{6, "platform/gen/artifactory", "artifacts/api/repo", code, 6, allow},
		{6, "platform/gen/access", "kernel/wire", code, 6, allow},
		{6, "platform/gen/artifactory", "artifacts/api", code, 7, violation},
		{6, "platform/blob", "artifacts/api/repo", code, 7, violation},

		{7, "platform/blob", "artifacts/api", code, 7, violation},
		{7, "kernel", "access", code, 7, violation},
		{7, "platform/httpkit", "gateway/rest", code, 7, violation},
		{7, "kernel/wire", "app/config", code, 7, violation},
		{7, "platform/work", "devtools/ledger", code, 7, violation},
		{7, "platform/work", "layout", test, 7, violation},
		{7, "platform/blob", "kernel", code, none, allow},
		{7, "platform/gen/artifactory", "platform/httpkit/apierror", code, none, allow},

		{8, "gateway/rest", "access/api", code, 8, allow},
		{8, "gateway/rest", "kernel/wire", code, 8, allow},
		{8, "gateway/rest", "platform/httpkit/apierror", code, 8, allow},
		{8, "gateway/rest/sub", "gateway/rest", code, 8, allow},
		{8, "gateway/rest", "platform/blob", code, 8, violation},
		{8, "gateway/rest", "access/core/token", code, 8, violation},
		{8, "gateway/rest", "pkg/client/access", code, 8, violation},

		{9, "access/api", "kernel", code, 9, allow},
		{9, "access/api", "platform/outbox", code, 9, allow},
		{9, "access/api/sub", "access/api", code, 9, allow},
		{9, "access/api", "platform/outbox/sql", code, 9, violation},
		{9, "access/api", "artifacts/api", code, 9, violation},
		{9, "access/api", "access/core/token", code, 9, violation},
		{9, "access/api", "pkg/client/access", code, 9, violation},

		{10, "artifacts/core/item", "access", code, 10, violation},
		{10, "access/core/user", "access", code, 10, violation},
		{10, "access", "artifacts", code, 10, violation},
		{10, "artifacts/core/item", "access/api", code, 11, allow},

		{11, "artifacts/core/item", "access/api/sub", code, 11, allow},
		{11, "artifacts/core/item", "access/core/token", code, 11, violation},
		{11, "artifacts/core/item", "access/adapters/sql", test, 11, violation},

		{12, "artifacts/core/item", "artifacts/adapters/sql", code, 12, violation},
		{12, "artifacts/core/item", "artifacts/adapters/sql", test, 12, violation},
		{12, "artifacts/adapters/http", "artifacts/adapters/sql", code, 14, allow},
		{12, "artifacts/core/item", "artifacts/core/port", code, 14, allow},

		{13, "artifacts/adapters/http", "artifacts/ecosystem/npm/http", code, 13, allow},
		{13, "artifacts/ecosystem/npm/http/sub", "artifacts/ecosystem/npm/http", code, 13, allow},
		{13, "artifacts/ecosystem/npm/http", "artifacts/ecosystem/npm/http/sub", code, 13, allow},
		{13, "artifacts/core/item", "artifacts/ecosystem/npm/http", code, 13, violation},
		{13, "artifacts/ecosystem/npm", "artifacts/ecosystem/npm/http", code, 13, violation},
		{13, "artifacts/ecosystem/pypi/http", "artifacts/ecosystem/npm/http", code, 13, violation},
		{13, "artifacts/ecosystem/pypi/http", "artifacts/ecosystem/npm/http", test, 14, allow},

		{14, "artifacts/core/item", "platform/blob", code, 14, allow},
		{14, "artifacts/core/item", "kernel/wire", code, 14, allow},
		{14, "artifacts/core/item", "pkg/client/artifactory", code, 14, allow},
		{14, "artifacts/core/item", "artifacts/ecosystem/npm", code, 14, allow},
		{14, "artifacts/core/item", "cmd/artifactweave", code, 16, violation},

		{15, "artifacts/core/item", "api", test, 15, allow},
		{15, "artifacts/core/item", "api", code, 16, violation},

		{16, "artifacts/core/item", "app/config", code, 16, violation},
		{16, "artifacts/core/item", "gateway/rest", code, 16, violation},
		{16, "artifacts/core/item", "devtools/ledger", code, 16, violation},
		{16, "artifacts/core/item", "layout", code, 16, violation},
		{16, "artifacts/core/item", "tools/stubgen", code, 16, violation},
		{16, "artifacts/core/item", "kernel", code, 14, allow},
	}

	t.Run("rows", func(t *testing.T) {
		for _, c := range cases {
			if row, ok := allowed(c.from, c.to, c.test); row != c.by || ok != c.ok {
				t.Errorf("allowed(%q, %q, %t) = row %d, %t; want row %d, %t",
					c.from, c.to, c.test, row, ok, c.by, c.ok)
			}
		}
	})

	t.Run("every row has both verdicts and decides a case", func(t *testing.T) {
		for _, c := range cases {
			if c.row < 0 || c.row >= len(rows) {
				t.Errorf("%s imports %s: row %d does not exist", c.from, c.to, c.row)
			}
		}
		for r := range rows {
			var allows, rejects, decides bool
			for _, c := range cases {
				allows = allows || c.row == r && c.ok
				rejects = rejects || c.row == r && !c.ok
				decides = decides || c.by == r
			}
			if !allows || !rejects || !decides {
				t.Errorf("row %d: allow case %t, violation case %t, decided case %t",
					r, allows, rejects, decides)
			}
		}
	})

	// src returns a Go file that imports paths.
	src := func(paths ...string) string {
		var b strings.Builder
		b.WriteString("package x\n\nimport (\n")
		for _, p := range paths {
			b.WriteString("\t_ " + strconv.Quote(p) + "\n")
		}
		b.WriteString(")\n")
		return b.String()
	}
	// tree writes files, path to content, under a new directory and returns it.
	tree := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for f, content := range files {
			write(t, filepath.Join(dir, filepath.FromSlash(f)), content)
		}
		return dir
	}
	l := &ledger.Ledger{
		Module:      modulePath,
		StagingRoot: root,
		Packages: []ledger.Package{
			{From: "internal/auth/token", Action: ledger.ActionMove, To: "access/core/token"},
			{
				From: "internal/fixture", Action: ledger.ActionMove, To: "test/fixture",
				Scope: ledger.ScopeRepo,
			},
			{From: "internal/layout", Action: ledger.ActionDelete},
			// Made up: pkgmove rewrites a real legacy path in this data once its
			// package moves. pkg/client may import its target.
			{From: "internal/repomodel", Action: ledger.ActionMove, To: "artifacts/api/repo"},
			{From: "pkg/types", Action: ledger.ActionSplit},
		},
	}
	const m = modulePath + "/"

	t.Run("the walk judges legacy imports by their ledger target", func(t *testing.T) {
		const item = "migration/artifacts/core/item/item.go"
		dir := tree(t, map[string]string{
			// Its own module once judged as the target, so allowed.
			"migration/access/core/user/user.go": src(m + "internal/auth/token"),
			item: src(
				"fmt", "example.com/elsewhere", m+"migration/artifacts/core/port",
				m+"migration/artifacts/adapters/sql", m+"internal/auth/token", m+"internal/fixture",
				m+"pkg/types", m+"internal/layout", m+"internal/unmapped",
			),
			"migration/artifacts/core/item/item_test.go": src(
				m+"migration/artifacts/core/item", m+"internal/fixture", m+"pkg/types",
			),
			// Each import below would break a row if the walk reached it.
			"migration/kernel/testdata/x.go":     src(m + "migration/app"),
			"migration/kernel/.cache/x.go":       src(m + "migration/app"),
			"migration/kernel/nested/go.mod":     "module example.com/nested\n",
			"migration/kernel/nested/x.go":       src(m + "migration/app"),
			"internal/server/server.go":          src(m + "test/server/support"),
			"pkg/client/artifactory/client.go":   src(m+"internal/repomodel", m+"pkg/types"),
			"pkg/containertest/containertest.go": src(m + "migration/platform/blob"),
		})
		violation := func(file, shown string, row int) string {
			return fmt.Sprintf("%s imports %s (row %d: %s)", file, shown, row, rows[row].text)
		}
		wantViolations := []string{
			violation(item, "internal/auth/token, judged as migration/access/core/token", 11),
			violation(item, "internal/fixture, judged as test/fixture", 2),
			item + " imports internal/layout (legacy package marked delete)",
			item + " imports internal/unmapped (legacy package with no ledger entry)",
			violation(item, "migration/artifacts/adapters/sql", 12),
			violation("pkg/client/artifactory/client.go", "pkg/types", 3),
			violation("pkg/containertest/containertest.go", "migration/platform/blob", 4),
		}
		wantSplits := []string{
			item + " imports pkg/types (legacy package marked split; allowed until it is emptied)",
			"migration/artifacts/core/item/item_test.go imports pkg/types " +
				"(legacy package marked split; allowed until it is emptied)",
		}
		violations, splits := checkImports(dir, l)
		if !slices.Equal(violations, wantViolations) {
			t.Errorf("violations:\n%q\nwant:\n%q", violations, wantViolations)
		}
		if !slices.Equal(splits, wantSplits) {
			t.Errorf("splits:\n%q\nwant:\n%q", splits, wantSplits)
		}
	})

	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "an unparsable file is a violation",
			files: map[string]string{
				"migration/kernel/broken.go":         "packag kernel\n",
				"pkg/client/artifactory/client.go":   "package artifactory\n",
				"pkg/containertest/containertest.go": "package containertest\n",
			},
			want: "migration/kernel/broken.go:1:1: expected 'package'",
		},
		{
			name: "a missing walk root is a violation",
			files: map[string]string{
				"migration/kernel/kernel.go":       "package kernel\n",
				"pkg/client/artifactory/client.go": "package artifactory\n",
			},
			want: "pkg/containertest",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			violations, splits := checkImports(tree(t, tc.files), l)
			if len(violations) != 1 || !strings.Contains(violations[0], tc.want) || splits != nil {
				t.Fatalf("violations = %q, splits = %q; want one violation containing %q",
					violations, splits, tc.want)
			}
		})
	}
}
