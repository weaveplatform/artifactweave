package cocoapods

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// The CDN index of a virtual repository lists every pod and version any
// member lists, once and sorted; all_pods.txt names each pod, and a shard
// leaves out a pod no member lists a version of.
func TestUnitCocoaPodsIndexMergeIsAUnion(t *testing.T) {
	merged, err := Index{}.Merge(t.Context(), []virtual.Part[Pods]{
		{Member: repo.Config{Key: "local"}, Doc: Pods{"Beta": {"2.0.0", "1.0.0"}, "Gone": nil}},
		{Member: repo.Config{Key: "remote"}, Doc: Pods{"Alpha": {"1.0.0"}, "Beta": {"1.0.0", "3.0.0"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.Lines(false); !slices.Equal(got, []string{"Alpha", "Beta", "Gone"}) {
		t.Fatalf("all_pods.txt = %v", got)
	}
	if got := merged.Lines(true); !slices.Equal(
		got, []string{"Alpha/1.0.0", "Beta/1.0.0/2.0.0/3.0.0"},
	) {
		t.Fatalf("shard = %v", got)
	}
	if got := (Pods{}).Lines(true); len(got) != 0 {
		t.Fatalf("empty shard = %v", got)
	}
}
