package pkgmove

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// TestUnitOwnershipRewriteKeepsTheInventoryBytes proves that the first move
// to touch the inventory changes only what moves: this repository's
// inventory, decoded and encoded again with every directory kept, comes back
// byte for byte, as tools/citestselection writes it.
func TestUnitOwnershipRewriteKeepsTheInventoryBytes(t *testing.T) {
	src := repopath.Read(t, ownershipPath)
	var o testOwnership
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		t.Fatal(err)
	}
	if got := encodeOwnership(o); !bytes.Equal(got, src) {
		t.Errorf("the inventory does not round-trip:\n%s", got)
	}
	keep := func(dir string) (string, map[string]string, bool) { return dir, nil, true }
	got, notices, err := rewriteOwnership(src, ownershipChange{mapDir: keep})
	if err != nil || len(notices) > 0 || !bytes.Equal(got, src) {
		t.Errorf(
			"a rewrite that keeps every directory changed the inventory: %v %q\n%s",
			err,
			notices,
			got,
		)
	}
}

func TestUnitOwnershipRewriteLeavesAnUnmovedInventory(t *testing.T) {
	src := []byte(
		`{"version":2,"packages":{"test/server":"shared"},"shared_files":{},"format_dependencies":{}}`,
	)
	got, notices, err := rewriteOwnership(src, ownershipChange{mapDir: mappingDirs(nil)})
	if err != nil || notices != nil || !bytes.Equal(got, src) {
		t.Errorf("got %q, %q, %v; want the source unchanged", got, notices, err)
	}
	_, _, err = rewriteOwnership(append(src, "{}"...), ownershipChange{mapDir: mappingDirs(nil)})
	if !errors.Is(err, errUnexpected) {
		t.Errorf("an inventory followed by more JSON: %v", err)
	}
}

func TestUnitMappingDirs(t *testing.T) {
	dirs := mappingDirs([]*mapping{
		{from: "internal/split"},
		{from: "internal/a", to: "migration/platform/a", rename: map[string]string{"x.go": "y.go"}},
		{from: "internal/gen", to: "migration/platform/gen", whole: true},
	})
	for _, c := range []struct {
		dir, want string
		ok        bool
	}{
		{"internal/split", "", false},
		{"internal/a", "migration/platform/a", true},
		{"internal/a/sub", "", false},
		{"internal/gen/x", "migration/platform/gen/x", true},
		{"internal/other", "", false},
	} {
		if got, _, ok := dirs(c.dir); got != c.want || ok != c.ok {
			t.Errorf("%s maps to %q, %v; want %q, %v", c.dir, got, ok, c.want, c.ok)
		}
	}
}

// TestUnitOwnershipMovesRootTests proves the inventory rules for a test file
// that a file entry moves on its own (docs/testing/server-test-ownership.md,
// "Moving a root test"): each case gives the inventory before the move and
// after it, with the notices.
func TestUnitOwnershipMovesRootTests(t *testing.T) {
	const dropped = "tools/citestselection/test_ownership.json: dropped "
	for _, c := range []struct {
		name, src string
		move      fileMove
		testsLeft bool
		want      string
		notices   []string
	}{
		{
			name: "an inventory without packages or shared files",
			src:  `{"version":2,"format_dependencies":{}}`,
			move: fileMove{"internal/server/a_test.go", "test/server/x/a_test.go"},
			want: `{"version":2,"packages":{"test/server/x":"shared"},` +
				`"shared_files":{"test/server/x":["a_test.go"]},"format_dependencies":{}}`,
		},
		{
			name: "the last listed test leaves a directory for its format's",
			src: `{"version":2,"packages":{"test/server":"shared"},` +
				`"shared_files":{"test/server":["a_test.go"]},` +
				`"format_dependencies":{"npm":["test/server/a_test.go"]}}`,
			move: fileMove{"test/server/a_test.go", "test/server/artifacts/npm/a_test.go"},
			want: `{"version":2,"packages":{"test/server/artifacts/npm":"npm"},` +
				`"shared_files":{},"format_dependencies":{}}`,
		},
		{
			name: "a test that leaves the owned directories drops its dependencies",
			src: `{"version":2,"packages":{"internal/server":"shared"},` +
				`"shared_files":{"internal/server":["a_test.go","b_test.go"]},` +
				`"format_dependencies":{"maven":["internal/server/a_test.go","internal/server/b_test.go"]}}`,
			move:      fileMove{"internal/server/a_test.go", "migration/platform/x/a_test.go"},
			testsLeft: true,
			want: `{"version":2,"packages":{"internal/server":"shared"},` +
				`"shared_files":{"internal/server":["b_test.go"]},` +
				`"format_dependencies":{"maven":["internal/server/b_test.go"]}}`,
			notices: []string{dropped + "internal/server/a_test.go from the format dependencies of maven, " +
				"since migration/platform/x is not a directory whose tests need an owner"},
		},
		{
			name: "a directory that one format owns drops another format's dependency",
			src: `{"version":2,"packages":{"test/server":"shared","test/server/artifacts/maven":"maven"},` +
				`"shared_files":{"test/server":["a_test.go"]},` +
				`"format_dependencies":{"maven":["test/server/a_test.go"],` +
				`"npm":["test/server/a_test.go","test/server/b_test.go"]}}`,
			move:      fileMove{"test/server/a_test.go", "test/server/artifacts/maven/a_test.go"},
			testsLeft: true,
			want: `{"version":2,"packages":{"test/server":"shared","test/server/artifacts/maven":"maven"},` +
				`"shared_files":{},"format_dependencies":{"npm":["test/server/b_test.go"]}}`,
			notices: []string{dropped + "test/server/a_test.go from the format dependencies of npm, " +
				"since test/server/artifacts/maven is owned by maven"},
		},
		{
			name: "a directory shared as a whole lists no file",
			src: `{"version":2,"packages":{"test/server":"shared","test/server/npm":"shared"},` +
				`"shared_files":{"test/server":["a_test.go","b_test.go"]},` +
				`"format_dependencies":{"npm":["test/server/a_test.go"]}}`,
			move:      fileMove{"test/server/a_test.go", "test/server/npm/a_test.go"},
			testsLeft: true,
			want: `{"version":2,"packages":{"test/server":"shared","test/server/npm":"shared"},` +
				`"shared_files":{"test/server":["b_test.go"]},"format_dependencies":{}}`,
			notices: []string{dropped + "test/server/a_test.go from the format dependencies of npm, " +
				"since test/server/npm is shared as a whole and lists no file"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var want testOwnership
			if err := json.Unmarshal([]byte(c.want), &want); err != nil {
				t.Fatal(err)
			}
			got, notices, err := rewriteOwnership([]byte(c.src), ownershipChange{
				mapDir:    mappingDirs(nil),
				tests:     []fileMove{c.move},
				testsLeft: func(string) bool { return c.testsLeft },
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, encodeOwnership(want)) {
				t.Errorf("got\n%s\nwant\n%s", got, encodeOwnership(want))
			}
			if !slices.Equal(notices, c.notices) {
				t.Errorf("notices %q, want %q", notices, c.notices)
			}
		})
	}
}

func TestUnitFormatOf(t *testing.T) {
	for dir, want := range map[string]string{
		"test/server/artifacts/npm":                 "npm",
		"migration/artifacts/ecosystem/maven/http":  "maven",
		"internal/artifacts/ecosystem/pypi/http":    "pypi",
		"migration/artifacts/ecosystem/maven/store": "",
		"pkg/artifacts/ecosystem/maven/http":        "",
		"test/server":                               "",
	} {
		if got := formatOf(dir); got != want {
			t.Errorf("formatOf(%s) = %q, want %q", dir, got, want)
		}
	}
}
