package swift

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// The release list of a virtual repository lists each version any member
// lists, once.
func TestUnitSwiftReleasesMergeIsAUnion(t *testing.T) {
	got, err := Releases{}.Merge(t.Context(), []virtual.Part[[]string]{
		{Member: repo.Config{Key: "local"}, Doc: []string{"1.0.0", "1.1.0"}},
		{Member: repo.Config{Key: "remote"}, Doc: []string{"2.0.0", "1.0.0"}},
	})
	if err != nil || !slices.Equal(got, []string{"1.0.0", "1.1.0", "2.0.0"}) {
		t.Fatalf("releases = %v, %v", got, err)
	}
}
