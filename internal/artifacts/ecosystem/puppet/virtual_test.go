package puppet

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// rel is one module release as a member lists it.
type rel struct {
	version, from string
	refused       bool
}

// TestUnitReleasesMergeServesARefusedVersionFromTheNextMember: the merge
// lists each version once, highest first, from the first member in the
// walk that lists it, unless that member's own patterns refuse its
// archive: then the first later member whose copy is not refused supplies
// it, and a version every member refuses keeps the first copy (R5,
// RTFACT-25571).
func TestUnitReleasesMergeServesARefusedVersionFromTheNextMember(t *testing.T) {
	parts := []virtual.Part[[]rel]{
		{Member: repo.Config{Key: "local"}, Doc: []rel{{"1.0.0", "local", true}, {"1.10.0", "local", false}, {"3.0.0", "local", true}}},
		{Member: repo.Config{Key: "remote"}, Doc: []rel{{"1.0.0", "remote", false}, {"1.10.0", "remote", false}, {"1.9.0", "remote", false}, {"3.0.0", "remote", true}}},
		{Member: repo.Config{Key: "other"}, Doc: []rel{{"1.0.0", "other", false}}},
	}
	got, err := Releases[rel]{
		Version: func(r rel) string { return r.version },
		Refused: func(r rel) bool { return r.refused },
	}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	want := []rel{{"3.0.0", "local", true}, {"1.10.0", "local", false}, {"1.9.0", "remote", false}, {"1.0.0", "remote", false}}
	if !slices.Equal(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	if got, err := (Releases[rel]{Version: func(r rel) string { return r.version }}).Merge(t.Context(), parts); err != nil ||
		got[3].from != "local" {
		t.Fatalf("merge without Refused = %v, %v", got, err)
	}
}
