package pkgmove

import (
	"bytes"
	"encoding/json"
	"errors"
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
	got, notices, err := rewriteOwnership(src, keep)
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
	got, notices, err := rewriteOwnership(src, mappingDirs(nil))
	if err != nil || notices != nil || !bytes.Equal(got, src) {
		t.Errorf("got %q, %q, %v; want the source unchanged", got, notices, err)
	}
	_, _, err = rewriteOwnership(append(src, "{}"...), mappingDirs(nil))
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
