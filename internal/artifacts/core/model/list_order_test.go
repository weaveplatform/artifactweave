package model_test

import (
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// newestFirst is the order the Puppet and Chef lists answered in before
// the index held it (#1426): x/mod/semver's, highest first, then the
// strings in byte order.
func newestFirst(a, b string) int {
	if c := semver.Compare("v"+a, "v"+b); c != 0 {
		return -c
	}
	return strings.Compare(a, b)
}

// galaxyList is the order Ansible's version list answered in before the
// index held it: the names in the index's Maven order, then byte order,
// through sort.Slice by x/mod/semver, highest first.
func galaxyList(names []string) []string {
	in := slices.Clone(names)
	slices.SortStableFunc(in, func(a, b string) int {
		if c := model.CompareMaven(a, b); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	sort.Slice(in, func(i, j int) bool { return semver.Compare("v"+in[i], "v"+in[j]) > 0 })
	return in
}

// stored is names as a list reads them from the index, highest OrderKey
// first, after ComparatorFor(t) has assigned the keys.
func stored(t *testing.T, pt repo.PackageType, names []string) []string {
	t.Helper()
	vs := pv(names...)
	model.AssignOrderKeys(model.ComparatorFor(pt), vs)
	keys := map[int64]bool{}
	for _, v := range vs {
		if keys[v.OrderKey] {
			t.Fatalf("%s: two versions share order key %d in %q", pt, v.OrderKey, names)
		}
		keys[v.OrderKey] = true
	}
	slices.SortFunc(vs, func(a, b model.PackageVersion) int { return int(b.OrderKey - a.OrderKey) })
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Version
	}
	return out
}

func TestUnitCompareForge(t *testing.T) {
	// Invalid versions sort first, ties reversed into byte order; build
	// metadata ties a release; Chef's two-part version ties its three-part
	// one.
	checkAscending(t, "forge", model.CompareForge, []string{
		"v1.0.0", "latest", "bogus", "1.0.0-", "01.2.3",
		"0.0.1",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11",
		"1.0.0+b", "1.0.0+a", "1.0.0",
		"1.2.0", "1.2",
		"1.10.0", "999999999.0.0",
	})
	// model.CompareSemver is not x/mod/semver: it reads a leading zero and
	// a leading "v", where the lists took those for invalid versions.
	if model.CompareSemver("01.2.3", "1.0.0") <= 0 || model.CompareForge("01.2.3", "1.0.0") >= 0 {
		t.Error("CompareForge must rank 01.2.3 as x/mod/semver does, below every valid version")
	}
}

func TestUnitCompareGalaxy(t *testing.T) {
	// Maven ranks 1.0.0 < 1.0.0+2 < 1.0.0+10 < 1.0.0+a < 1.0.0+b, and
	// the ties are reversed; invalid versions tie in byte order, reversed.
	checkAscending(t, "galaxy", model.CompareGalaxy, []string{
		"latest", "bogus", "+2", "0.0.1", "1.0.0-rc.1+b", "1.0.0-rc.1",
		"1.0.0+b", "1.0.0+a", "1.0.0+10", "1.0.0+2", "1.0.0",
		"2.0.0",
	})
}

// TestUnitListOrderKeys checks, over random version sets, that the order
// keys Puppet, Chef and Ansible take read back, highest first, in the order
// their lists answered in when they sorted every name: Puppet's and Chef's
// for any strings, Ansible's for versions it admits, twelve or fewer, whose
// order its unstable sort kept.
func TestUnitListOrderKeys(t *testing.T) {
	rnd := rand.New(rand.NewPCG(4, 1426))
	parts := []string{
		"0", "1", "2", "10", "1.0", "1.0.0", "1.2.0", "1.10.0", "2.0.0", "1.2", "01.0.0",
		"-rc.1", "-rc.2", "-alpha", "-1", "+a", "+b", "+2", "+10", "bogus", "latest",
	}
	anything := func() string {
		v := parts[rnd.IntN(len(parts))]
		for rnd.IntN(2) == 0 {
			v += parts[rnd.IntN(len(parts))]
		}
		return v
	}
	pick := func(from ...string) string { return from[rnd.IntN(len(from))] }
	admitted := func() string {
		v := pick("0", "1", "2", "10") + "." + pick("0", "1", "10") + "." + pick("0", "1", "2")
		if rnd.IntN(3) == 0 {
			v += "-" + pick("alpha", "beta", "rc", "1", "2", "10", "x-y")
			if rnd.IntN(2) == 0 {
				v += "." + pick("1", "2", "10", "a")
			}
		}
		if rnd.IntN(2) == 0 {
			v += "+" + pick("a", "b", "2", "10", "001", "a.b", "x-1")
		}
		return v
	}
	set := func(version func() string) []string {
		seen := map[string]bool{}
		var names []string
		for range 1 + rnd.IntN(12) {
			if v := version(); !seen[v] {
				seen[v] = true
				names = append(names, v)
			}
		}
		rnd.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		return names
	}
	for round := range 2000 {
		names := set(anything)
		want := slices.SortedFunc(slices.Values(names), newestFirst)
		for _, pt := range []repo.PackageType{repo.Puppet, repo.Chef} {
			if got := stored(t, pt, names); !slices.Equal(got, want) {
				t.Fatalf("round %d, %s: %q, want %q", round, pt, got, want)
			}
		}
		names = set(admitted)
		if got, want := stored(t, repo.Ansible, names), galaxyList(names); !slices.Equal(got, want) {
			t.Fatalf("round %d, ansible: %q, want %q", round, got, want)
		}
	}
}
