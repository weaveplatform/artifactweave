package buildinfo

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// The Go linker ignores -X for a symbol that does not exist, so a renamed
// variable or a moved package would ship "devel" without any error. Taking
// the addresses makes a rename fail to compile here, and the test below
// fails when a build stamps a symbol that is not one of these.
var _ = []*string{&version, &commit, &buildDate}

// marker gives the test this package's import path without spelling it out.
type marker struct{}

var stampedSymbol = regexp.MustCompile(`-X\s+(\S+)\.(\w+)=`)

func TestUnitLdflagsStampThisPackage(t *testing.T) {
	root := repopath.Root(t)
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module := modfile.ModulePath(goMod)
	pkg := reflect.TypeFor[marker]().PkgPath()
	for _, build := range []struct {
		file    string
		symbols []string
	}{
		{"Makefile", []string{"buildDate", "commit", "version"}},
		{".goreleaser.yaml", []string{"buildDate", "commit", "version"}},
		{"Dockerfile", []string{"commit", "version"}},
	} {
		source, err := os.ReadFile(filepath.Join(root, build.file))
		if err != nil {
			t.Fatal(err)
		}
		var symbols []string
		for _, m := range stampedSymbol.FindAllStringSubmatch(string(source), -1) {
			target := m[1]
			for _, variable := range []string{"$(MODULE)/", "${MODULE}/"} {
				if rel, ok := strings.CutPrefix(target, variable); ok {
					target = module + "/" + rel
				}
			}
			if target != pkg {
				t.Errorf("%s: -X %s.%s stamps %s, not %s", build.file, m[1], m[2], target, pkg)
			}
			symbols = append(symbols, m[2])
		}
		slices.Sort(symbols)
		if symbols = slices.Compact(symbols); !slices.Equal(symbols, build.symbols) {
			t.Errorf("%s stamps %v, want %v", build.file, symbols, build.symbols)
		}
	}
}
