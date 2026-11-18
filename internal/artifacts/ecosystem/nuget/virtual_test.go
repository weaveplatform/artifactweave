package nuget_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
)

func TestUnitVirtualVersionOwnershipAndNuGetOrder(t *testing.T) {
	part := func(priority bool, vs ...string) virtual.Part[nuget.Package[string]] {
		p := virtual.Part[nuget.Package[string]]{Member: repo.Config{PriorityResolution: repo.Flex(priority)}, Doc: nuget.Package[string]{ID: "Acme.Mixed"}}
		for _, v := range vs {
			p.Doc.Versions = append(p.Doc.Versions, nuget.Contribution[string]{Version: v, Path: v, Value: v})
		}
		return p
	}
	first := part(false, "1.0", "2.0.0-RC.1", "10.0.0", "invalid")
	second := part(false, "1.0.0+build", "2.0.0", "3.0.0")
	got, err := (nuget.Merger[string]{}).Merge(t.Context(), []virtual.Part[nuget.Package[string]]{first, second})
	if err != nil {
		t.Fatal(err)
	}
	keys, owners := []string{}, []string{}
	for _, v := range got.Versions {
		keys = append(keys, v.Version)
		owners = append(owners, v.Value)
	}
	if got.ID != "acme.mixed" || !reflect.DeepEqual(keys, []string{"1.0.0", "2.0.0-rc.1", "2.0.0", "3.0.0", "10.0.0"}) || owners[0] != "1.0" {
		t.Fatalf("merged %+v", got)
	}
	// A priority id shadows the whole non-priority id before virtual filtering.
	got, err = (nuget.Merger[string]{Allowed: func(path string) bool { return path != "4.0.0" }}).Merge(
		t.Context(),
		[]virtual.Part[nuget.Package[string]]{first, part(true, "4.0.0"), second},
	)
	if err != nil || len(got.Versions) != 0 {
		t.Fatalf("priority filtering: %+v %v", got, err)
	}
	got, err = (nuget.Merger[string]{}).Merge(t.Context(), []virtual.Part[nuget.Package[string]]{part(true), first, part(true, "3.0.0"), part(true, "3.0.0+other", "4.0.0")})
	if err != nil || len(got.Versions) != 2 || got.Versions[0].Value != "3.0.0" {
		t.Fatalf("priority union %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = (nuget.Merger[string]{}).Merge(ctx, []virtual.Part[nuget.Package[string]]{first}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUnitVirtualSearchKeepsFirstIDPositionAndNewestWholeRecord(t *testing.T) {
	pkg := func(id, v, owner string) nuget.Package[string] {
		return nuget.Package[string]{ID: id, Versions: []nuget.Contribution[string]{{Version: v, Value: owner}}}
	}
	parts := []virtual.Part[[]nuget.Package[string]]{
		{Doc: []nuget.Package[string]{pkg("Zeta", "1.0.0", "first"), pkg("Alpha", "1.0.0", "alpha")}},
		{Doc: []nuget.Package[string]{pkg("ZETA", "2.0.0", "new"), pkg("Beta", "3.0.0", "beta"), pkg("Empty", "invalid", "")}},
	}
	got, err := (nuget.SearchMerger[string]{}).Merge(t.Context(), parts)
	if err != nil || len(got) != 3 || got[0].ID != "zeta" || got[1].ID != "alpha" || got[2].ID != "beta" || got[0].Versions[1].Value != "new" {
		t.Fatalf("search %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = (nuget.SearchMerger[string]{}).Merge(ctx, parts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
