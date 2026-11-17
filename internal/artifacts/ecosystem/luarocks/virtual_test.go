package luarocks

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// TestUnitMergerKeepsTheFirstMembersRecordOfEachFile: the merge lists every
// file its members' manifests list, in the walk's order, and the record of a
// file two members list is the earlier member's; another architecture or
// version of the same package is a file of its own.
func TestUnitMergerKeepsTheFirstMembersRecordOfEachFile(t *testing.T) {
	entry := func(name, version, arch, from string) Entry[string] {
		return Entry[string]{Name: name, Version: version, Arch: arch, Record: from}
	}
	parts := []virtual.Part[[]Entry[string]]{
		{Member: repo.Config{Key: "local"}, Doc: []Entry[string]{
			entry("lpeg", "1.1.0-1", "rockspec", "local"),
		}},
		{Member: repo.Config{Key: "rocks"}, Doc: []Entry[string]{
			entry("lpeg", "1.1.0-1", "rockspec", "remote"),
			entry("lpeg", "1.1.0-1", "src", "remote"),
			entry("lpeg", "1.0.2-1", "rockspec", "remote"),
		}},
	}
	got, err := Merger[string]{}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, e := range got {
		listed = append(listed, e.Name+" "+e.Version+" "+e.Arch+" "+e.Record)
	}
	want := []string{
		"lpeg 1.1.0-1 rockspec local", "lpeg 1.1.0-1 src remote", "lpeg 1.0.2-1 rockspec remote",
	}
	if !slices.Equal(listed, want) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	if got, err := (Merger[string]{}).Merge(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
