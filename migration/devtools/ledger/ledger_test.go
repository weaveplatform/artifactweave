package ledger

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sample returns a small ledger that validates and uses every kind of entry,
// action and scope. Each call returns a fresh copy for a test to change. Its
// split package, internal/web, is made up, outside every entry of the
// repository's ledger, so that no move rewrites it and pkgmove check never
// reports it.
func sample() *Ledger {
	return &Ledger{
		Schema:      schemaVersion,
		Module:      module,
		StagingRoot: stagingRoot,
		FinalRoot:   finalRoot,
		Packages: []Package{
			{From: "internal/web", Action: ActionSplit, Ticket: "WEB-22"},
			{
				From: "internal/web/xyz", Action: ActionMove, To: "artifacts/ecosystem/npm/http",
				Name: "npmhttp", Ticket: "ECO-06",
			},
			{
				From: "internal/formatregistry", Action: ActionMove, To: "artifacts/ecosystem",
				Rename: map[string]string{"formats.go": "registry.go"}, Ticket: "ART-05",
			},
			{From: "internal/layout", Action: ActionDelete, Ticket: "PRO-02"},
			{From: "internal/a", Action: ActionMerge, To: "platform/ab", Ticket: "PLT-03"},
			{From: "internal/b", Action: ActionMerge, To: "platform/ab", Ticket: "PLT-03"},
			{
				From:   "internal/fakeserver",
				Action: ActionMove,
				Scope:  ScopeRepo,
				To:     "test/server/support/fake",
				Ticket: "TST-01",
			},
		},
		Files: []File{
			{
				From: "internal/web/npm_test.go", Action: ActionMove, Scope: ScopeRepo,
				To: "test/server/artifacts/npm", Ticket: "TST-05",
			},
			{
				From:   "internal/web/testdata/npm/",
				Action: ActionMove,
				To:     "artifacts/ecosystem/npm/http/testdata",
				Ticket: "ECO-06",
			},
			{
				From:   "internal/web/handlers.go",
				Action: ActionSplit,
				Targets: []string{
					"artifacts/adapters/http",
					"access/adapters/http",
				},
				Ticket: "WEB-03",
			},
			{From: "internal/web/legacy.go", Action: ActionDelete, Ticket: "WEB-22"},
		},
		Placeholders: []Placeholder{
			{To: "artifacts/core/virtual", Phases: []string{"P10"}},
		},
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnitLoadRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	dir := t.TempDir()
	canonical := string(Format(sample()))

	path := filepath.Join(dir, "ledger.json")
	writeFile(t, path, canonical+"\n\t \n")
	l, err := Load(path)
	if err != nil {
		t.Fatalf("Load of a canonical ledger with trailing white space: %v", err)
	}
	if errs := l.Validate(); errs != nil || string(Format(l)) != canonical {
		t.Errorf("loaded ledger: errors %v, formats as\n%s", errs, Format(l))
	}

	for _, tc := range []struct {
		name, content, want string
		is                  error
	}{
		{"missing file", "", "", fs.ErrNotExist},
		{"unknown field", strings.Replace(canonical, `"from"`, `"form"`, 1), `unknown field "form"`, nil},
		{"second object", canonical + "{}\n", "continues after the ledger object", ErrInvalid},
		{"stray brace", canonical + "}", "continues after the ledger object", ErrInvalid},
		{"not JSON", "schema: 1\n", "invalid character", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.json")
			if !errors.Is(tc.is, fs.ErrNotExist) {
				writeFile(t, path, tc.content)
			}
			l, err := Load(path)
			if l != nil || err == nil {
				t.Fatalf("Load = %v, %v; want an error", l, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load error %q does not contain %q", err, tc.want)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("Load error %v does not wrap %v", err, tc.is)
			}
		})
	}
}

func TestUnitTranslate(t *testing.T) {
	l := sample()
	for _, tc := range []struct {
		importPath, target, action string
		ok                         bool
	}{
		{module + "/internal/web/xyz", module + "/migration/artifacts/ecosystem/npm/http", ActionMove, true},
		{module + "/internal/formatregistry", module + "/migration/artifacts/ecosystem", ActionMove, true},
		{module + "/internal/a", module + "/migration/platform/ab", ActionMerge, true},
		{module + "/internal/fakeserver", module + "/test/server/support/fake", ActionMove, true},
		{module + "/internal/web", "", ActionSplit, true},
		{module + "/internal/layout", "", ActionDelete, true},
		{module + "/internal/nothing", "", "", false},
		{module + "/migration/artifacts/ecosystem", "", "", false},
		{"internal/web/xyz", "", "", false},
		{"net/http", "", "", false},
	} {
		target, action, ok := l.Translate(tc.importPath)
		if target != tc.target || action != tc.action || ok != tc.ok {
			t.Errorf("Translate(%s) = %q, %q, %v; want %q, %q, %v",
				tc.importPath, target, action, ok, tc.target, tc.action, tc.ok)
		}
	}
}

func TestUnitDirResolvesScopes(t *testing.T) {
	l := sample()
	for _, tc := range []struct{ scope, to, want string }{
		{"", "platform/x", "migration/platform/x"},
		{ScopeRoot, "platform/x", "migration/platform/x"},
		{ScopeRepo, "test/server/x", "test/server/x"},
	} {
		if got := l.Dir(tc.scope, tc.to); got != tc.want {
			t.Errorf("Dir(%q, %q) = %q, want %q", tc.scope, tc.to, got, tc.want)
		}
	}
}

func TestUnitFileTargetKeepsNamesAndAssetDirectories(t *testing.T) {
	l := sample()
	for _, tc := range []struct {
		f    File
		want string
	}{
		{File{From: "internal/x/a.go", To: "platform/y"}, "migration/platform/y/a.go"},
		{File{From: "internal/x/a.go", To: "platform/y", File: "b.go"}, "migration/platform/y/b.go"},
		{File{From: "internal/x/testdata/npm/", To: "platform/y/testdata"}, "migration/platform/y/testdata/npm/"},
		{File{From: "internal/x/x_test.go", Scope: ScopeRepo, To: "test/server/x"}, "test/server/x/x_test.go"},
	} {
		if got := l.FileTarget(tc.f); got != tc.want {
			t.Errorf("FileTarget(%+v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}

func TestUnitLegacyRootsAgreeWithPatterns(t *testing.T) {
	// legacyRoots groups the pkg/ roots, as LegacyRegexp's documented form
	// does; rebuilding it from LegacyRoots proves the two list the same
	// directories.
	var plain, pkg []string
	for _, root := range LegacyRoots {
		if rest, ok := strings.CutPrefix(root, "pkg/"); ok {
			pkg = append(pkg, rest)
		} else {
			plain = append(plain, root)
		}
	}
	if want := "(" + strings.Join(
		append(plain, "pkg/("+strings.Join(pkg, "|")+")"),
		"|",
	) + ")"; legacyRoots != want {
		t.Fatalf("legacyRoots = %s; LegacyRoots give %s", legacyRoots, want)
	}
	if got, want := LegacyRegexp(), `^(internal|pkg/(aql|blob|cdn|layout|repo|store|types))/`; got != want {
		t.Errorf("LegacyRegexp() = %s; want %s", got, want)
	}

	re := regexp.MustCompile(LegacyRegexp())
	for _, root := range LegacyRoots {
		for _, rel := range []string{root, root + "/x"} {
			if !IsLegacy(rel) || !re.MatchString(rel+"/") || !legacyRe.MatchString(rel) {
				t.Errorf("IsLegacy or a legacy pattern rejects the directory %s", rel)
			}
		}
		for _, rel := range []string{root + "/x_test.go", root + "/x/testdata/"} {
			if !IsLegacy(rel) || !legacyFileRe.MatchString(rel) {
				t.Errorf("IsLegacy or the legacy file pattern rejects %s", rel)
			}
		}
	}
	for _, rel := range []string{
		"pkg", "pkg/client/access", "pkg/containertest", "pkg/blobber", "internalx", "cmd/artifactweave",
		"migration/internal", "test/internal/x", "tools/pkg/blob",
	} {
		if IsLegacy(rel) || re.MatchString(rel+"/") || legacyRe.MatchString(rel) {
			t.Errorf("%s is not legacy, but IsLegacy or a legacy pattern accepts it", rel)
		}
	}
}
