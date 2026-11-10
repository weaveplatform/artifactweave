package pkgmove

import (
	"path"
	"strings"
	"testing"
)

// never reports no directory and no file, for matchers whose tests do not
// depend on the tree.
func never(string) bool { return false }

// applyRefs returns text with each reference replaced, or marked as
// {?old} when it has no single replacement.
func applyRefs(text string, refs []ref) string {
	var b strings.Builder
	last := 0
	for _, r := range refs {
		b.WriteString(text[last:r.start])
		if r.repl == "" {
			b.WriteString("{?" + text[r.start:r.end] + "}")
		} else {
			b.WriteString(r.repl)
		}
		last = r.end
	}
	b.WriteString(text[last:])
	return b.String()
}

func TestUnitMatcherBoundaries(t *testing.T) {
	mt := newMatcher(
		"github.com/weaveplatform/artifactweave",
		[]*mapping{
			{
				from:   "internal/alpha",
				to:     "migration/platform/beta",
				rename: map[string]string{"old.go": "new.go"},
				ticket: "PLT-01",
			},
			{
				from:   "internal/alpha/sub",
				to:     "migration/platform/beta/sub",
				whole:  true,
				ticket: "PLT-01",
			},
			{from: "internal/gen", to: "migration/platform/gen", whole: true, ticket: "PLT-02"},
			{from: "internal/split", ticket: "PLT-03"},
		},
		func(dir string) bool { return dir == "internal/alpha/pkg" },
		func(p string) bool { return p == "internal/alpha.go" },
	)
	for _, c := range []struct{ name, text, want string }{
		{"whole text", "internal/alpha", "migration/platform/beta"},
		{"sibling prefix", "internal/alphabet internal/alpha_x internal/alpha-x", "internal/alphabet internal/alpha_x internal/alpha-x"},
		{"inside a longer path", "pkg/x/internal/alpha xinternal/alpha", "pkg/x/internal/alpha xinternal/alpha"},
		{"sentence end", "see internal/alpha.", "see migration/platform/beta."},
		{"tracked file named like the package", "internal/alpha.go", "internal/alpha.go"},
		{"-X flag", "-X internal/alpha.Version=1", "-X migration/platform/beta.Version=1"},
		{"import form", `"github.com/weaveplatform/artifactweave/internal/alpha"`, `"github.com/weaveplatform/artifactweave/migration/platform/beta"`},
		{"Makefile variable", "$(MODULE)/internal/alpha.v ${MODULE}/internal/alpha/x.go", "$(MODULE)/migration/platform/beta.v ${MODULE}/migration/platform/beta/x.go"},
		{"anchor", "^internal/alpha/a\\.go ^internal/alpha$", "^migration/platform/beta/a\\.go ^migration/platform/beta$"},
		{"relative chain", "../../internal/alpha/x.go ./internal/alpha", "../../migration/platform/beta/x.go ./migration/platform/beta"},
		{"file below", "internal/alpha/testdata/f.txt", "migration/platform/beta/testdata/f.txt"},
		{"rename", "internal/alpha/old.go internal/alpha/old.golden", "migration/platform/beta/new.go migration/platform/beta/old.golden"},
		{"subpackage with its own entry", "internal/alpha/pkg/y.go internal/alpha/pkg", "internal/alpha/pkg/y.go internal/alpha/pkg"},
		{"the longest old path wins", "internal/alpha/sub/z.go internal/alpha/sub", "migration/platform/beta/sub/z.go migration/platform/beta/sub"},
		{"dot elements", "internal/alpha/. internal/alpha/../x", "internal/alpha/. internal/alpha/../x"},
		{"dots that end a sentence", "see internal/alpha/README.", "see migration/platform/beta/README."},
		{"prefix of a package that does not move whole", `"internal/alpha/" internal/alpha/ ./internal/alpha/...`, `"internal/alpha/" internal/alpha/ ./internal/alpha/...`},
		{"prefix of a package that moves whole", `"internal/gen/" ./internal/gen/... internal/gen/$$s/x.go internal/gen/*`, `"migration/platform/gen/" ./migration/platform/gen/... migration/platform/gen/$$s/x.go migration/platform/gen/*`},
		{"prefix at the end of the text", "internal/gen/", "migration/platform/gen/"},
		{"no single replacement", "internal/split internal/split/x.go", "{?internal/split} {?internal/split/x.go}"},
		{"followers", "`internal/alpha`:internal/alpha)internal/alpha=internal/alpha\\x", "`migration/platform/beta`:migration/platform/beta)migration/platform/beta=migration/platform/beta\\x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := applyRefs(c.text, mt.find(c.text)); got != c.want {
				t.Errorf("rewrite of %q\n got %q\nwant %q", c.text, got, c.want)
			}
		})
	}
}

func TestUnitModuleOnlyMappingNeedsTheModule(t *testing.T) {
	mt := newMatcher(
		"github.com/weaveplatform/artifactweave",
		[]*mapping{{from: "migration", to: "internal", whole: true, moduleOnly: true}},
		never,
		never,
	)
	text := "github.com/weaveplatform/artifactweave/migration/gateway ${MODULE}/migration/... migration/gateway migrations/ the migration"
	want := "github.com/weaveplatform/artifactweave/internal/gateway ${MODULE}/internal/... migration/gateway migrations/ the migration"
	if got := applyRefs(text, mt.find(text)); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestUnitFileEntriesMatchTheirOwnPaths proves how file entries and the
// package entry of their directory share the paths below it: a file entry
// matches its file and nothing below it, an asset directory's entry
// everything below it, and the package entry what neither holds.
func TestUnitFileEntriesMatchTheirOwnPaths(t *testing.T) {
	mt := newMatcher(
		"github.com/weaveplatform/artifactweave",
		[]*mapping{
			fileMapping("internal/alpha/a.go", "migration/platform/beta/a.go", "PLT-05"),
			fileMapping("internal/alpha/assets/", "migration/platform/beta/assets/", "PLT-05"),
			{from: "internal/alpha", to: "migration/platform/alpha", ticket: "PLT-06"},
		},
		never,
		never,
	)
	mt.fileEntry = func(p string) bool {
		for ; p != "."; p = path.Dir(p) {
			if p == "internal/alpha/a.go" || p == "internal/alpha/assets" {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ name, text, want string }{
		{"the file", "see internal/alpha/a.go.", "see migration/platform/beta/a.go."},
		{"below the file", "internal/alpha/a.go/x", "internal/alpha/a.go/x"},
		{"the asset directory", "internal/alpha/assets/logo.txt", "migration/platform/beta/assets/logo.txt"},
		{"another file of the package", "internal/alpha/b.go", "migration/platform/alpha/b.go"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := applyRefs(c.text, mt.find(c.text)); got != c.want {
				t.Errorf("rewrite of %q\n got %q\nwant %q", c.text, got, c.want)
			}
		})
	}
}
