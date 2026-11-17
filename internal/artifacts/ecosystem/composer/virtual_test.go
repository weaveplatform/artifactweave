package composer

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

type testRelease struct{ version, from string }

// The p2 metadata of a virtual repository is the union of its members'
// releases: a version two members list is the first member's, and a
// version only a later member lists is kept, from that member.
func TestUnitComposerReleasesMergeKeepsTheFirstMembersRelease(t *testing.T) {
	m := Releases[testRelease]{Version: func(r testRelease) string { return r.version }}
	got, err := m.Merge(t.Context(), []virtual.Part[[]testRelease]{
		{Member: repo.Config{Key: "local"}, Doc: []testRelease{{"1.1.0", "local"}, {"1.0.0", "local"}}},
		{Member: repo.Config{Key: "remote"}, Doc: []testRelease{{"2.0.0", "remote"}, {"1.0.0", "remote"}}},
	})
	want := []testRelease{{"1.1.0", "local"}, {"1.0.0", "local"}, {"2.0.0", "remote"}}
	if err != nil || len(got) != len(want) {
		t.Fatalf("releases = %v, %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("releases = %v, want %v", got, want)
		}
	}
}
