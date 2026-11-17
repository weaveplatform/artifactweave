package ansible

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// rel is one collection version as a member lists it.
type rel struct{ version, from string }

// TestUnitReleasesMergeKeepsTheFirstMembersCopyNewestFirst: the merge lists
// each version once, from the first member in the walk that lists it, the
// copy the archive is served from, newest first by semantic version, as
// Galaxy's version list answers.
func TestUnitReleasesMergeKeepsTheFirstMembersCopyNewestFirst(t *testing.T) {
	parts := []virtual.Part[[]rel]{
		{Member: repo.Config{Key: "local"}, Doc: []rel{{"1.2.3", "local"}, {"1.10.0", "local"}}},
		{Member: repo.Config{Key: "remote"}, Doc: []rel{{"1.2.3", "remote"}, {"2.0.0-rc.1", "remote"}, {"1.9.0", "remote"}}},
	}
	got, err := Releases[rel]{Version: func(r rel) string { return r.version }}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	want := []rel{{"2.0.0-rc.1", "remote"}, {"1.10.0", "local"}, {"1.9.0", "remote"}, {"1.2.3", "local"}}
	if !slices.Equal(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	if got, err := (Releases[rel]{Version: func(r rel) string { return r.version }}).Merge(t.Context(), nil); err != nil ||
		len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
