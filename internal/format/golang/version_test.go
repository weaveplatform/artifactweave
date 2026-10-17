package golang_test

import (
	"slices"
	"testing"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ascending holds the examples of the Go Modules Reference ("Versions" and
// "Pseudo-versions") and of semver.org's precedence rule, in order. A
// pseudo-version sorts above its base and below the next release, and
// +incompatible is build metadata.
var ascending = []string{
	"v0.0.0-20191109021931-daa7c04131f5",
	"v1.0.0-alpha",
	"v1.0.0-alpha.1",
	"v1.0.0-alpha.beta",
	"v1.0.0-beta",
	"v1.0.0-beta.2",
	"v1.0.0-beta.11",
	"v1.0.0-rc.1",
	"v1.0.0",
	"v1.2.3-pre",
	"v1.2.3-pre.0.20191109021931-daa7c04131f5",
	"v1.2.3",
	"v1.2.4-0.20191109021931-daa7c04131f5",
	"v1.2.4",
	"v1.10.0",
	"v2.0.0+incompatible",
	"v4.1.2+incompatible",
}

// The package index orders Go versions as golang.org/x/mod/semver does,
// which is the order the go command uses.
func TestOrder(t *testing.T) {
	c := types.ComparatorFor(repo.Go)
	for i, a := range ascending {
		for j, b := range ascending {
			got := c.Compare(types.PackageVersion{Version: a}, types.PackageVersion{Version: b})
			want := semver.Compare(a, b)
			if sign(got) != want || want != sign(i-j) {
				t.Errorf("compare(%s, %s) = %d; semver says %d", a, b, got, want)
			}
		}
	}
	if got := c.Compare(
		types.PackageVersion{Version: "v2.0.0+incompatible"},
		types.PackageVersion{Version: "v2.0.0"},
	); got != 0 {
		t.Errorf("+incompatible changed the order: %d", got)
	}
}

func sign(n int) int {
	return min(max(n, -1), 1)
}

// @v/list leaves pseudo-versions out and sorts the rest.
func TestListed(t *testing.T) {
	got := golang.Listed([]string{
		"v2.0.0+incompatible", "v1.10.0", "v0.0.0-20191109021931-daa7c04131f5", "v1.2.0",
		"v1.2.4-0.20191109021931-daa7c04131f5", "v1.2.0-rc.1",
	})
	want := []string{"v1.2.0-rc.1", "v1.2.0", "v1.10.0", "v2.0.0+incompatible"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v; want %v", got, want)
	}
	if got := golang.Listed([]string{"v0.0.0-20191109021931-daa7c04131f5"}); len(got) != 0 {
		t.Fatalf("pseudo-versions only: %v", got)
	}
}

// @latest prefers a release to a pre-release, and a pre-release to a
// pseudo-version, whatever their order; among pseudo-versions the latest
// commit wins.
func TestLatest(t *testing.T) {
	for _, c := range []struct {
		versions []string
		want     string
	}{
		{[]string{
			"v1.0.0", "v1.3.0-rc.1", "v1.2.4-0.20191109021931-daa7c04131f5", "v1.1.0",
		}, "v1.1.0"},
		{[]string{"v1.0.0", "v2.0.0+incompatible"}, "v2.0.0+incompatible"},
		{[]string{
			"v1.0.0-rc.2", "v1.0.0-rc.10", "v1.0.1-0.20191109021931-daa7c04131f5",
		}, "v1.0.0-rc.10"},
		{[]string{
			"v1.2.4-0.20180101000000-aaaaaaaaaaaa",
			"v0.0.0-20200101000000-bbbbbbbbbbbb",
			"v0.0.0-20190101000000-cccccccccccc",
		}, "v0.0.0-20200101000000-bbbbbbbbbbbb"},
		{[]string{
			"v0.0.0-20200101000000-bbbbbbbbbbbb",
			"v0.0.1-0.20200101000000-aaaaaaaaaaaa",
		}, "v0.0.1-0.20200101000000-aaaaaaaaaaaa"},
		{[]string{
			"v0.0.0-20191309021931-daa7c04131f5",
			"v0.0.0-20000101000000-daa7c04131f5",
		}, "v0.0.0-20000101000000-daa7c04131f5"},
	} {
		got, ok := golang.Latest(c.versions)
		if !ok || got != c.want {
			t.Errorf("%v: %s, %v; want %s", c.versions, got, ok, c.want)
		}
	}
	if got, ok := golang.Latest(nil); ok {
		t.Errorf("no versions: %s", got)
	}
}
