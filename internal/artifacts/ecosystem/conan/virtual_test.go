package conan_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conan"
)

func TestUnitVirtualRevisionMerge(t *testing.T) {
	first := conan.Revision{Revision: "a", Time: time.Unix(1, 0)}
	parts := []virtual.Part[[]conan.Revision]{
		{Doc: []conan.Revision{first}},
		{Doc: []conan.Revision{{Revision: "a", Time: time.Unix(9, 0)}, {Revision: "b", Time: time.Unix(2, 0)}}},
	}
	got, err := (conan.Merger{}).Merge(t.Context(), parts)
	if err != nil || len(got) != 2 || got[0].Revision != "b" || got[1] != first {
		t.Fatalf("%+v %v", got, err)
	}
	empty, err := (conan.Merger{}).Merge(t.Context(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	if parts[0].Doc[0] != first {
		t.Fatal("merge changed member metadata")
	}
}

func TestUnitVirtualSearchMerges(t *testing.T) {
	got, err := (conan.SearchMerger{}).Merge(t.Context(), []virtual.Part[[]string]{{Doc: []string{"b", "a"}}, {Doc: []string{"b", "c"}}})
	if err != nil || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatal(got, err)
	}
	parts := []virtual.Part[map[string]json.RawMessage]{
		{Doc: map[string]json.RawMessage{"one": json.RawMessage(`"first"`)}},
		{Doc: map[string]json.RawMessage{"one": json.RawMessage(`"second"`), "two": json.RawMessage(`"other"`)}},
	}
	packages, err := (conan.PackageMerger{}).Merge(t.Context(), parts)
	if err != nil || string(packages["one"]) != `"first"` || len(packages) != 2 {
		t.Fatal(packages, err)
	}
	packages["one"][0] = 'x'
	if string(parts[0].Doc["one"]) != `"first"` {
		t.Fatal("merge aliases member data")
	}
}
