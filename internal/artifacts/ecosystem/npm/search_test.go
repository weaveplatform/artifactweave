package npm

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestUnitSearchMergeOwnershipAndPaging(t *testing.T) {
	parts := []virtual.Part[[]SearchHit[string]]{
		{Doc: []SearchHit[string]{{Name: "duplicate", Value: "first complete owner"}, {Name: "local", Value: "hosted"}}},
		{
			Doc: []SearchHit[string]{
				{Name: "duplicate", Value: "later"},
				{Name: "remote", Value: "non-SemVer version"},
				{Name: "remote", Value: "internal duplicate"},
				{Name: "last", Value: "last"},
			},
		},
	}
	for _, tc := range []struct {
		from, size int
		names      []string
	}{{0, 10, []string{"duplicate", "local", "remote", "last"}}, {1, 2, []string{"local", "remote"}}, {4, 20, []string{}}, {math.MaxInt, math.MaxInt, []string{}}, {0, 0, []string{}}} {
		got, err := (SearchMerger[string]{From: tc.from, Size: tc.size}).Merge(t.Context(), parts)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, hit := range got {
			names = append(names, hit.Name)
		}
		if !reflect.DeepEqual(names, tc.names) {
			t.Fatal(tc, names)
		}
		if tc.from == 0 && tc.size > 0 && got[0].Value != "first complete owner" {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (SearchMerger[string]{Size: 1}).Merge(ctx, parts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUnitSafeSearchVersion(t *testing.T) {
	for _, version := range []string{"1.0.0", "Unity-1.0.0.0", "not-semver", "1.0.0+build", ".."} {
		if !SafeSearchVersion(version) {
			t.Fatal(version)
		}
	}
	for _, version := range []string{"", "../escape", "bad\\path", "bad;property", "bad?query", "bad#fragment", "%2fescape", "bad\nvalue", "\x00"} {
		if SafeSearchVersion(version) {
			t.Fatal(version)
		}
	}
}
