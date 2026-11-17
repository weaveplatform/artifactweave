package pub

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

type mergedRelease struct{ version, from string }

func (r mergedRelease) Version() string { return r.version }

// TestUnitMergerListsEachVersionOnceNewestFirst: the merge lists every
// version its members list, a version two members list described as the
// earlier member has it, newest first by SemVer 2.0 and, for versions that
// differ only in build metadata, by their text.
func TestUnitMergerListsEachVersionOnceNewestFirst(t *testing.T) {
	parts := []virtual.Part[[]mergedRelease]{
		{Member: repo.Config{Key: "pub-a"}, Doc: []mergedRelease{
			{"1.0.0", "a"}, {"2.0.0-beta.1", "a"}, {"1.0.0+b", "a"},
		}},
		{Member: repo.Config{Key: "pub-b"}, Doc: []mergedRelease{
			{"1.5.0", "b"}, {"1.0.0", "b"}, {"1.0.0+a", "b"},
		}},
	}
	got, err := Merger[mergedRelease]{Version: mergedRelease.Version}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	want := []mergedRelease{
		{"2.0.0-beta.1", "a"}, {"1.5.0", "b"}, {"1.0.0", "a"}, {"1.0.0+a", "b"}, {"1.0.0+b", "a"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	got, err = Merger[mergedRelease]{Version: mergedRelease.Version}.Merge(t.Context(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
