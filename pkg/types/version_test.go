package types_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// checkAscending asserts every earlier version sorts strictly before every
// later one, in both argument orders, so the comparator is antisymmetric
// along the list.
func checkAscending(t *testing.T, name string, cmpFn func(a, b string) int, versions []string) {
	t.Helper()
	for i := range versions {
		for j := i + 1; j < len(versions); j++ {
			if c := cmpFn(versions[i], versions[j]); c >= 0 {
				t.Errorf("%s: %q vs %q = %d, want < 0", name, versions[i], versions[j], c)
			}
			if c := cmpFn(versions[j], versions[i]); c <= 0 {
				t.Errorf("%s: %q vs %q = %d, want > 0", name, versions[j], versions[i], c)
			}
		}
		if c := cmpFn(versions[i], versions[i]); c != 0 {
			t.Errorf("%s: %q vs itself = %d", name, versions[i], c)
		}
	}
}

func checkEqual(t *testing.T, name string, cmpFn func(a, b string) int, pairs [][2]string) {
	t.Helper()
	for _, p := range pairs {
		if c := cmpFn(p[0], p[1]); c != 0 {
			t.Errorf("%s: %q vs %q = %d, want 0", name, p[0], p[1], c)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	checkAscending(t, "semver", types.CompareSemver, []string{
		"1.0.0-", "1.x", "bogus", "latest",
		"v0.0.0-20190101000000-abcdef123456",
		"0.0.1",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		"v1.0.1", "1.2", "1.10.0", "2.0.0",
		"18446744073709551616.0.0",
	})
	checkEqual(t, "semver", types.CompareSemver, [][2]string{
		{"1.0.0+build.5", "1.0.0"},
		{"v1.2.3", "1.2.3"},
		{"1.2", "1.2.0"},
		{"01.2.3", "1.2.3"},
		{"1..0", "1..0"},
	})
	if types.CompareSemver("1.2.3.4", "0.0.1") >= 0 {
		t.Error("four components are not semver and should sort first")
	}
}

func TestCompareNuGet(t *testing.T) {
	checkAscending(t, "nuget", types.CompareNuGet, []string{
		"1.0.0.0.0", "v1.0",
		"1.0.0-alpha", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.10", "1.0.0",
		"1.0.0.1", "1.0.1", "2.0",
	})
	checkEqual(t, "nuget", types.CompareNuGet, [][2]string{
		{"1.0", "1.0.0.0"}, {"1.0.0-BETA", "1.0.0-beta"}, {"1.0.0+meta", "1.0.0"},
	})
	if types.CompareNuGet("1.0.0-", "1.0.0") >= 0 {
		t.Error("an empty pre-release label is invalid and should sort first")
	}
}

func TestComparePEP440(t *testing.T) {
	checkAscending(t, "pep440", types.ComparePEP440, []string{
		"1.0.x",
		"1.0.dev456", "1.0a1", "1.0a2.dev456", "1.0a12.dev456", "1.0a12",
		"1.0b1.dev456", "1.0b2", "1.0b2.post345.dev456", "1.0b2.post345",
		"1.0rc1.dev456", "1.0rc1", "1.0", "1.0+abc.5", "1.0+abc.7", "1.0+5",
		"1.0.post456.dev34", "1.0.post456", "1.0.15", "1.1.dev1", "1!0.1",
	})
	checkEqual(t, "pep440", types.ComparePEP440, [][2]string{
		{"1.0", "1.0.0"},
		{"1.0alpha1", "1.0a1"},
		{"1.0-1", "1.0.post1"},
		{"1.0c1", "1.0rc1"},
		{"V1.0", "1.0"},
		{" 1.0 ", "1.0"},
		{"1.0.post", "1.0.post0"},
		{"1.0+abc", "1.0+ABC"},
		{"1.0b", "1.0b0"},
		{"1.0pre1", "1.0rc1"},
	})
	if types.ComparePEP440("1.0+abc", "1.0+abc.1") >= 0 {
		t.Error("a longer local label should sort after its prefix")
	}
}

func TestCompareMaven(t *testing.T) {
	checkAscending(t, "maven", types.CompareMaven, []string{
		"1-alpha2snapshot", "1-alpha2", "1-alpha-123", "1-beta-2", "1-beta123",
		"1-m2", "1-m11", "1-rc", "1-cr2", "1-rc123", "1-SNAPSHOT", "1", "1-sp",
		"1-sp2", "1-sp123", "1-abc", "1-def", "1-pom-1", "1-1-snapshot", "1-1",
		"1-2", "1-123",
	})
	checkAscending(t, "maven", types.CompareMaven, []string{
		"1.0-alpha1", "1.0-beta1", "1.0-milestone1", "1.0-rc1", "1.0-SNAPSHOT",
		"1.0", "1.0-sp1", "1.0.1", "1.1", "1.9", "1.10", "1.10.1", "2.0",
		"20240101.0.0", "99999999999999999999",
	})
	checkEqual(t, "maven", types.CompareMaven, [][2]string{
		{"1", "1.0"},
		{"1", "1.0.0"},
		{"1", "1-ga"},
		{"1", "1.0-final"},
		{"1", "1-RELEASE"},
		{"1-cr1", "1-rc1"},
		{"1-a1", "1-alpha1"},
		{"1-b1", "1-beta-1"},
		{"1..1", "1.0.1"},
	})
}

// TestCompareMercury follows Mercury's ComparableVersion, which Artifactory's
// versions searches sort with (#214): snapshot ranks below every other
// qualifier, release is no alias, and a '-' opens a sublist only between
// digits.
func TestCompareMercury(t *testing.T) {
	checkAscending(t, "mercury", types.CompareMercury, []string{
		"1.0-SNAPSHOT", "1.0-alpha-1", "1.0-alpha-2", "1.0-beta-1", "1.0-milestone-1",
		"1.0-rc1", "1.0-cr2", "1.0", "1.0-sp1", "1.0-release", "1.0-xyz", "1.0-1",
		"1.0-20260101.120000-1", "1.0-20260101.120000-2", "1.0-20260101.120000-10",
		"1.0.1", "1.1", "1.9", "1.10", "1.10.1", "2.0", "20240101.0.0",
		"99999999999999999999",
	})
	checkEqual(t, "mercury", types.CompareMercury, [][2]string{
		{"1", "1.0"},
		{"1", "1.0.0"},
		{"1", "1-ga"},
		{"1", "1.0-final"},
		{"1", "1-0"},
		{"1-cr1", "1-rc1"},
		{"1-a1", "1-alpha1"},
		{"1-b1", "1-beta-1"},
		{"1-m1", "1-milestone.1"},
		{"1..1", "1.0.1"},
		{"1.01", "1.1"},
		{"1.0-alpha-1", "1.0-alpha.1"},
		{"1.0-SNAPSHOT", "1-snapshot"},
	})
	// Where Mercury and Maven's ComparableVersion part ways.
	for _, tc := range []struct {
		a, b           string
		mercury, maven int
	}{
		{"1.0-SNAPSHOT", "1.0-rc1", -1, 1},
		{"1.0-SNAPSHOT", "1.0-alpha-1", -1, 1},
		{"1.0-release", "1.0", 1, 0},
		{"1.0-alpha-1", "1.0-alpha.1", 0, -1},
	} {
		if got := types.CompareMercury(tc.a, tc.b); got != tc.mercury {
			t.Errorf("mercury %q vs %q = %d, want %d", tc.a, tc.b, got, tc.mercury)
		}
		if got := types.CompareMaven(tc.a, tc.b); got != tc.maven {
			t.Errorf("maven %q vs %q = %d, want %d", tc.a, tc.b, got, tc.maven)
		}
	}
}

func TestComparatorForAndSorting(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		pt   repo.PackageType
		in   []types.PackageVersion
		want []string
	}{
		{repo.NPM, pv("1.10.0", "1.2.0", "1.2.0-rc.1"), []string{"1.2.0-rc.1", "1.2.0", "1.10.0"}},
		{
			repo.AgentPackages,
			pv("1.10.0", "1.2.0", "1.2.0-rc.1"),
			[]string{"1.2.0-rc.1", "1.2.0", "1.10.0"},
		},
		{repo.Go, pv("v1.10.0", "v1.9.0"), []string{"v1.9.0", "v1.10.0"}},
		{repo.PyPI, pv("1.0", "1.0rc1", "1.0.post1"), []string{"1.0rc1", "1.0", "1.0.post1"}},
		{repo.NuGet, pv("1.0.0", "1.0.0-beta"), []string{"1.0.0-beta", "1.0.0"}},
		// The Maven family orders by Mercury, which ranks a snapshot below
		// every qualifier (decision 0036); other types keep Maven's
		// ComparableVersion, which puts it just below the release.
		{
			repo.Maven,
			pv("1.0", "1.0-alpha-1", "1.0-SNAPSHOT"),
			[]string{"1.0-SNAPSHOT", "1.0-alpha-1", "1.0"},
		},
		{
			repo.Gradle,
			pv("1.0", "1.0-alpha-1", "1.0-SNAPSHOT"),
			[]string{"1.0-SNAPSHOT", "1.0-alpha-1", "1.0"},
		},
		{repo.Ivy, pv("1.0-rc1", "1.0-SNAPSHOT"), []string{"1.0-SNAPSHOT", "1.0-rc1"}},
		{repo.SBT, pv("1.10", "1.9-SNAPSHOT"), []string{"1.9-SNAPSHOT", "1.10"}},
		{repo.Generic, pv("1.10", "1.9"), []string{"1.9", "1.10"}},
		{
			repo.Generic,
			pv("1.0", "1.0-alpha-1", "1.0-SNAPSHOT"),
			[]string{"1.0-alpha-1", "1.0-SNAPSHOT", "1.0"},
		},
		{repo.Docker, []types.PackageVersion{
			{Version: "latest", Created: t0.Add(time.Hour)},
			{Version: "b", Created: t0},
			{Version: "a", Created: t0},
		}, []string{"a", "b", "latest"}},
		// A Helm OCI chart's tags are images' tags (decision 0018): pushed
		// later sorts later, whatever the version strings say.
		{repo.HelmOCI, []types.PackageVersion{
			{Version: "1.10.0", Created: t0},
			{Version: "1.9.0", Created: t0.Add(time.Hour)},
		}, []string{"1.10.0", "1.9.0"}},
		{repo.Helm, pv("1.10.0", "1.9.0"), []string{"1.9.0", "1.10.0"}},
		{repo.Skills, pv("1.10.0", "1.9.0"), []string{"1.9.0", "1.10.0"}},
	}
	for _, tc := range cases {
		c := types.ComparatorFor(tc.pt)
		types.SortVersions(c, tc.in)
		var got []string
		for _, v := range tc.in {
			got = append(got, v.Version)
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.pt, diff)
		}
	}
}

func TestAssignOrderKeys(t *testing.T) {
	vs := pv("1.0.0", "2.0.0", "1.0.0+build", "0.9.0")
	types.AssignOrderKeys(types.ComparatorFor(repo.NPM), vs)
	got := map[string]int64{}
	for _, v := range vs {
		got[v.Version] = v.OrderKey
	}
	want := map[string]int64{"0.9.0": 1, "1.0.0": 2, "1.0.0+build": 2, "2.0.0": 3}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("order keys (-want +got):\n%s", diff)
	}
}

func pv(versions ...string) []types.PackageVersion {
	out := make([]types.PackageVersion, len(versions))
	for i, v := range versions {
		out[i] = types.PackageVersion{Version: v}
	}
	return out
}
