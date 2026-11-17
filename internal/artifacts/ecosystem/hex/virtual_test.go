package hex_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
)

func TestUnitVirtualRegistryMerge(t *testing.T) {
	one := hex.RegistryPackage{
		Name:       "hello",
		Repository: "origin",
		Releases:   []hex.Package{{Version: "1.0.0", AdvisoryIndexes: []uint64{0}, Requirements: []hex.Requirement{{Repository: "origin"}}}},
		Advisories: [][]byte{[]byte("one")},
	}
	two := hex.RegistryPackage{
		Name:       "hello",
		Repository: "second",
		Releases:   []hex.Package{{Version: "1.0.0", AdvisoryIndexes: []uint64{0}}, {Version: "2.0.0", AdvisoryIndexes: []uint64{1}}},
		Advisories: [][]byte{[]byte("hidden"), []byte("two")},
	}
	parts := []virtual.Part[map[string]hex.RegistryPackage]{{Doc: map[string]hex.RegistryPackage{"hello": one}}, {Doc: map[string]hex.RegistryPackage{"hello": two}}}
	got, err := (hex.Merger{Repository: "virtual"}).Merge(t.Context(), parts)
	reg := got["hello"]
	if err != nil || len(reg.Releases) != 2 || len(reg.Advisories) != 2 || string(reg.Advisories[1]) != "two" || reg.Releases[1].AdvisoryIndexes[0] != 1 ||
		reg.Releases[0].Requirements[0].Repository != "virtual" {
		t.Fatal(reg, err)
	}
	if one.Releases[0].Requirements[0].Repository != "origin" || two.Releases[1].AdvisoryIndexes[0] != 1 {
		t.Fatal("merge changed member metadata")
	}
	empty, err := (hex.Merger{}).Merge(t.Context(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
}
