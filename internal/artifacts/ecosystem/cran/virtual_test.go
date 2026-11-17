package cran

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// TestUnitMergerKeepsTheGreatestVersionOfEachName: the merge lists each
// package name once, at the greatest version any member lists, from the
// first member that lists that version, in the order names are first
// listed; names that differ in case stay apart.
func TestUnitMergerKeepsTheGreatestVersionOfEachName(t *testing.T) {
	row := func(name, version, from string) Package {
		return Package{Name: name, Version: version, Path: from + "/" + name + "_" + version}
	}
	parts := []virtual.Part[[]Package]{
		{Member: repo.Config{Key: "local"}, Doc: []Package{row("alpha", "1.0", "local"), row("beta", "2.0", "local")}},
		{Member: repo.Config{Key: "remote"}, Doc: []Package{
			row("alpha", "1.10", "remote"), row("beta", "2.0", "remote"), row("Beta", "0.1", "remote"),
		}},
	}
	got, err := Merger{}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, p := range got {
		listed = append(listed, p.Path)
	}
	want := []string{"remote/alpha_1.10", "local/beta_2.0", "remote/Beta_0.1"}
	if !slices.Equal(listed, want) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	if got, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
