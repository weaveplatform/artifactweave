package gems

import (
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// TestUnitMergerKeepsTheFirstMembersCopyOfEachGem: the merge lists every
// gem file its members list, in the walk's order, and a file two members
// list comes from the earlier one, the copy the gem download serves.
func TestUnitMergerKeepsTheFirstMembersCopyOfEachGem(t *testing.T) {
	gem := func(name, version, from string, at int64) Release {
		p := Package{Name: name, Version: version, Description: from}
		p.Path = "gems/" + Filename(p)
		return Release{Package: p, Created: time.Unix(at, 0)}
	}
	parts := []virtual.Part[[]Release]{
		{Member: repo.Config{Key: "gems-local"}, Doc: []Release{
			gem("rake", "13.0.0", "local", 1), gem("rack", "3.0.0", "local", 2),
		}},
		{Member: repo.Config{Key: "gems-remote"}, Doc: []Release{
			gem("rake", "13.0.0", "remote", 0), gem("rake", "12.3.1", "remote", 0),
		}},
	}
	got, err := Merger{}.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, r := range got {
		listed = append(listed, r.Path+" "+r.Description)
	}
	want := []string{
		"gems/rake-13.0.0.gem local", "gems/rack-3.0.0.gem local", "gems/rake-12.3.1.gem remote",
	}
	if !slices.Equal(listed, want) || !got[0].Created.Equal(time.Unix(1, 0)) {
		t.Fatalf("merged %v, want %v", listed, want)
	}
	if got, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("merge of no member = %v, %v", got, err)
	}
}
