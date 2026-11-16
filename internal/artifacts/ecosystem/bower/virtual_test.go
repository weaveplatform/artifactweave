package bower

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func parts(docs ...[]string) []virtual.Part[[]string] {
	out := make([]virtual.Part[[]string], 0, len(docs))
	for i, d := range docs {
		out = append(out, virtual.Part[[]string]{Member: repo.Config{Key: string(rune('a' + i))}, Doc: d})
	}
	return out
}

// The package list names each package once, sorted, whichever members list
// it.
func TestUnitBowerNamesMergeIsASortedUnion(t *testing.T) {
	got, err := Names{}.Merge(t.Context(), parts(
		[]string{"zeta", "alpha"}, nil, []string{"alpha", "mid"},
	))
	if err != nil || !slices.Equal(got, []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("names = %v, %v", got, err)
	}
	if got, _ := (Names{}).Merge(t.Context(), nil); len(got) != 0 {
		t.Fatalf("no parts = %v", got)
	}
}

// A package's versions keep the walk's order, each from the first member
// that holds it, so the archive is served from that member.
func TestUnitBowerVersionsMergeKeepsTheWalksOrder(t *testing.T) {
	got, err := Versions{}.Merge(t.Context(), parts(
		[]string{"2.0.0", "1.0.0"}, []string{"1.0.0", "3.0.0"},
	))
	if err != nil || !slices.Equal(got, []string{"2.0.0", "1.0.0", "3.0.0"}) {
		t.Fatalf("versions = %v, %v", got, err)
	}
}
