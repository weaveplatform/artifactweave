package rpm

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// TestUnitMergerKeepsTheFirstMembersRowOfEachPath: the merge lists every
// package its members list, in the walk's order, and a path two members
// list comes from the earlier one, the copy a GET through the virtual
// repository serves.
func TestUnitMergerKeepsTheFirstMembersRowOfEachPath(t *testing.T) {
	row := func(path, from string) Package { return Package{Path: path, Summary: from} }
	parts := []virtual.Part[[]Package]{
		{Member: repo.Config{Key: "local"}, Doc: []Package{row("pool/alpha-1.0-1.noarch.rpm", "local"), row("pool/beta-1.0-1.noarch.rpm", "local")}},
		{Member: repo.Config{Key: "remote"}, Doc: []Package{row("pool/alpha-1.0-1.noarch.rpm", "remote"), row("pool/gamma-1.0-1.noarch.rpm", "remote")}},
	}
	got, err := Merger{}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, p := range got {
		listed = append(listed, p.Path+" "+p.Summary)
	}
	want := []string{"pool/alpha-1.0-1.noarch.rpm local", "pool/beta-1.0-1.noarch.rpm local", "pool/gamma-1.0-1.noarch.rpm remote"}
	if !slices.Equal(listed, want) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	if got, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
