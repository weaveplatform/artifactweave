package chef

import (
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// rel is one cookbook version as a member lists it, with the members
// whose later copies the merge recorded.
type rel struct {
	version, from string
	also          []string
}

// TestUnitReleasesMergeKeepsTheFirstMembersCopyAndRecordsTheOthers: the
// merge lists each version once, from the first member in the walk that
// lists it, highest first, and hands each later copy to Also, which the
// handler uses to fall back to a later member for the archive.
func TestUnitReleasesMergeKeepsTheFirstMembersCopyAndRecordsTheOthers(t *testing.T) {
	parts := []virtual.Part[[]rel]{
		{Member: repo.Config{Key: "local"}, Doc: []rel{{version: "1.0.0", from: "local"}, {version: "1.10.0", from: "local"}}},
		{Member: repo.Config{Key: "remote"}, Doc: []rel{{version: "1.0.0", from: "remote"}, {version: "1.9.0", from: "remote"}}},
	}
	m := Releases[rel]{
		Version: func(r rel) string { return r.version },
		Also: func(first, later rel) rel {
			first.also = append(slices.Clone(first.also), later.from)
			return first
		},
	}
	got, err := m.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, r := range got {
		listed = append(listed, r.version+" "+r.from+" also:"+strings.Join(r.also, ","))
	}
	want := []string{"1.10.0 local also:", "1.9.0 remote also:", "1.0.0 local also:remote"}
	if !slices.Equal(listed, want) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	m.Also = nil
	if got, err := m.Merge(t.Context(), parts); err != nil || len(got) != 3 || got[2].from != "local" {
		t.Fatalf("merge without Also = %v, %v", got, err)
	}
}
