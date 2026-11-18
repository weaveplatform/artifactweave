package terraform

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestUnitModuleVersionsKeepFirstWholeOwner(t *testing.T) {
	parts := []virtual.Part[[]ModuleVersion[string]]{
		{Doc: []ModuleVersion[string]{{Version: "2.0.0", Value: "first"}, {Version: "1.0.0+z", Value: "build-z"}, {Version: "1.0.0+a", Value: "build-a"}}},
		{Doc: []ModuleVersion[string]{{Version: "2.0.0", Value: "later"}, {Version: "1.0.0-beta", Value: "prerelease"}, {Version: "1.10.0", Value: "minor"}}},
	}
	want := []ModuleVersion[string]{
		{Version: "1.0.0-beta", Value: "prerelease"},
		{Version: "1.0.0+a", Value: "build-a"},
		{Version: "1.0.0+z", Value: "build-z"},
		{Version: "1.10.0", Value: "minor"},
		{Version: "2.0.0", Value: "first"},
	}
	for range 20 {
		got, err := (ModuleMerger[string]{}).Merge(t.Context(), parts)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
	if got, err := (ModuleMerger[string]{}).Merge(t.Context(), nil); len(got) != 0 || err != nil {
		t.Fatal(got)
	}
}

func TestUnitModuleMergeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (ModuleMerger[string]{}).Merge(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
