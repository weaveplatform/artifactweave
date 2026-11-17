package golang

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestUnitVirtualVersionUnion(t *testing.T) {
	parts := []virtual.Part[[]string]{
		{Doc: []string{"v1.1.0 2026-10-01T00:00:00Z", "v1.0.0", "v0.0.0-20261016120000-abcdefabcdef"}},
		{Doc: []string{"v1.1.0 2026-10-02T00:00:00Z", "v1.2.0-rc.1", "v1.2.0"}},
	}
	got, err := (Merger{}).Merge(t.Context(), parts)
	want := []string{"v0.0.0-20261016120000-abcdefabcdef", "v1.0.0", "v1.1.0 2026-10-01T00:00:00Z", "v1.2.0-rc.1", "v1.2.0"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, %v; want %v", got, err, want)
	}
	versions := make([]string, 0, len(got))
	for _, line := range got {
		versions = append(versions, LineVersion(line))
	}
	if latest, ok := Latest(versions); !ok || latest != "v1.2.0" {
		t.Fatalf("latest %q, %v", latest, ok)
	}
	if out, err := (Merger{}).Merge(t.Context(), nil); err != nil || len(out) != 0 {
		t.Fatalf("empty: %v, %v", out, err)
	}
	if LineVersion(" \t") != "" {
		t.Fatal("blank line has a version")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = (Merger{}).Merge(ctx, parts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
