package bazelmodules_test

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bazelmodules"
)

func TestUnitVirtualMetadataMerge(t *testing.T) {
	parts := []virtual.Part[bazelmodules.VersionMetadata]{
		{Doc: bazelmodules.VersionMetadata{Versions: []string{"1.0.0"}}},
		{
			Doc: bazelmodules.VersionMetadata{
				Versions:       []string{"2.0.0", "1.0.0"},
				YankedVersions: map[string]string{"1.0.0": "shadowed", "2.0.0": "withdrawn"},
				Deprecated:     "successor",
			},
		},
	}
	got, err := (bazelmodules.Merger{}).Merge(t.Context(), parts)
	if err != nil || !slices.Equal(got.Versions, []string{"1.0.0", "2.0.0"}) || got.YankedVersions["1.0.0"] != "" || got.YankedVersions["2.0.0"] != "withdrawn" ||
		got.Deprecated != "successor" {
		t.Fatal(got, err)
	}
	empty, err := (bazelmodules.Merger{}).Merge(t.Context(), nil)
	if err != nil || len(empty.Versions) != 0 {
		t.Fatal(empty, err)
	}
}
