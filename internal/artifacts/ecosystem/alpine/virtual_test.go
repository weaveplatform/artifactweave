package alpine

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// TestUnitMergerKeepsTheFirstMembersRowOfEachPath: the merge lists every
// package its members list, in the walk's order, and a path two members
// list comes from the earlier one, the copy a GET through the virtual
// repository serves.
func TestUnitMergerKeepsTheFirstMembersRowOfEachPath(t *testing.T) {
	row := func(path, from string) Package { return Package{Path: path, Description: from} }
	parts := []virtual.Part[[]Package]{
		{Member: repo.Config{Key: "local"}, Doc: []Package{row("v3.20/main/x86_64/hello-1.0-r0.apk", "local"), row("v3.20/main/x86_64/curl-8.0-r0.apk", "local")}},
		{Member: repo.Config{Key: "remote"}, Doc: []Package{row("v3.20/main/x86_64/hello-1.0-r0.apk", "remote"), row("v3.20/main/x86_64/zlib-1.3-r0.apk", "remote")}},
	}
	got, err := Merger{}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, p := range got {
		listed = append(listed, p.Path+" "+p.Description)
	}
	want := []string{"v3.20/main/x86_64/hello-1.0-r0.apk local", "v3.20/main/x86_64/curl-8.0-r0.apk local", "v3.20/main/x86_64/zlib-1.3-r0.apk remote"}
	if !slices.Equal(listed, want) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	if got, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
