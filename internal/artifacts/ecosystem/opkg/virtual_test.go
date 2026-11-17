package opkg

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
	row := func(path, from string) Package { return Package{Path: path, Description: from} }
	parts := []virtual.Part[[]Package]{
		{Member: repo.Config{Key: "local"}, Doc: []Package{row("feed/alpha_1.0_all.ipk", "local"), row("feed/beta_1.0_all.ipk", "local")}},
		{Member: repo.Config{Key: "remote"}, Doc: []Package{row("feed/alpha_1.0_all.ipk", "remote"), row("feed/gamma_1.0_all.ipk", "remote")}},
	}
	got, err := Merger{}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, p := range got {
		listed = append(listed, p.Path+" "+p.Description)
	}
	want := []string{"feed/alpha_1.0_all.ipk local", "feed/beta_1.0_all.ipk local", "feed/gamma_1.0_all.ipk remote"}
	if !slices.Equal(listed, want) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	if got, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
