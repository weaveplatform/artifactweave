package virtual_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// release is one version a member lists in a test format's document.
type release struct{ Version, URL, From string }

// index is the test format's document: its releases and its latest.
type index struct {
	Releases []release
	Latest   string
}

// testMerger is a format's merge, built only from the contract's helpers,
// the way a family's virtual.go is meant to read.
type testMerger struct{ base, api, key string }

var _ virtual.Merger[index] = testMerger{}

func (m testMerger) Merge(_ context.Context, parts []virtual.Part[index]) (index, error) {
	lists := make([][]release, len(parts))
	for i, p := range parts {
		for _, r := range p.Doc.Releases {
			r.From = p.Member.Key
			lists[i] = append(lists[i], r)
		}
	}
	all := virtual.UnionBy(lists, func(r release) string { return semver.Canonical(r.Version) })
	virtual.SortVersions(all, func(r release) string { return r.Version }, semver.Compare)
	versions := make([]string, len(all))
	for i := range all {
		all[i].URL = virtual.APIBase(m.base, m.api, m.key) + "/files/" + all[i].Version
		versions[i] = all[i].Version
	}
	stable := func(v string) bool { return semver.Prerelease(v) == "" }
	return index{Releases: all, Latest: virtual.Latest(versions, semver.Compare, stable)}, nil
}

func spell(rs []release) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version + "@" + r.From
	}
	return strings.Join(out, " ")
}

// TestUnitCollectMerge: Collect reads the members in the walk's order,
// leaves out each member whose read passes over or misses, and a Merger
// built from the helpers keeps the first member's copy of a version, sorts
// the versions with equal ones in the walk's order, picks the latest
// release over a later pre-release, and points every file at the virtual
// repository's own API.
func TestUnitCollectMerge(t *testing.T) {
	members := []virtual.Member{
		{Repo: local("a-local", false)},
		{Repo: local("unreadable", false)},
		{Repo: local("absent", false)},
		{Repo: remote("b-remote", false), Upstream: true},
	}
	docs := map[string]index{
		"a-local":  {Releases: []release{{Version: "v1.0.0"}, {Version: "v2.0.0-beta.1"}}},
		"b-remote": {Releases: []release{{Version: "v1.0.0"}, {Version: "v1.5.0"}, {Version: "v1.0"}}},
	}
	var asked []string
	parts, err := virtual.Collect(t.Context(), members,
		func(_ context.Context, m virtual.Member) (index, error) {
			asked = append(asked, m.Repo.Key)
			switch m.Repo.Key {
			case "unreadable":
				return index{}, fmt.Errorf("read: %w", virtual.ErrPassOver)
			case "absent":
				return index{}, fmt.Errorf("read: %w", kernel.ErrNotFound)
			}
			return docs[m.Repo.Key], nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a-local", "unreadable", "absent", "b-remote"}; !slices.Equal(asked, want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	if len(parts) != 2 || parts[0].Member.Key != "a-local" || parts[1].Member.Key != "b-remote" {
		t.Fatalf("parts %+v", parts)
	}
	got, err := testMerger{base: "https://ext.example/", api: "testfmt", key: "virt"}.
		Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	// v1.0 is v1.0.0 to SemVer: its key repeats a-local's version, so it
	// is dropped, and a-local's copy of v1.0.0 wins over b-remote's.
	if want := "v1.0.0@a-local v1.5.0@b-remote v2.0.0-beta.1@a-local"; spell(got.Releases) != want {
		t.Errorf("releases %s, want %s", spell(got.Releases), want)
	}
	if got.Latest != "v1.5.0" {
		t.Errorf("latest %s, want v1.5.0", got.Latest)
	}
	if want := "https://ext.example/artifactory/api/testfmt/virt/files/v1.5.0"; got.Releases[1].URL != want {
		t.Errorf("url %s, want %s", got.Releases[1].URL, want)
	}
}

// TestUnitCollectFault: a member whose read fails for any reason but a
// pass-over or a miss ends the walk, naming the member, so a fault never
// shrinks a merge silently.
func TestUnitCollectFault(t *testing.T) {
	fault := errors.New("disk on fire")
	asked := 0
	parts, err := virtual.Collect(t.Context(),
		[]virtual.Member{{Repo: local("broken", false)}, {Repo: local("after", false)}},
		func(context.Context, virtual.Member) (string, error) {
			asked++
			return "", fault
		})
	if !errors.Is(err, fault) || !strings.Contains(err.Error(), "member broken") ||
		parts != nil || asked != 1 {
		t.Fatalf("parts %v, err %v, asked %d", parts, err, asked)
	}
}

func TestUnitUnionBy(t *testing.T) {
	got := virtual.UnionBy([][]string{{"a1", "b1"}, nil, {"b2", "c2", "a2"}, {"c3"}},
		func(s string) byte { return s[0] })
	if want := []string{"a1", "b1", "c2"}; !slices.Equal(got, want) {
		t.Fatalf("union %v, want %v", got, want)
	}
	if got := virtual.UnionBy[string, string](nil, strings.ToLower); len(got) != 0 {
		t.Fatalf("union of nothing %v", got)
	}
}

// TestUnitSortVersions: versions a comparison counts equal, two builds of
// one SemVer version here, keep the order they came in.
func TestUnitSortVersions(t *testing.T) {
	for _, in := range [][]string{
		{"v2.0.0", "v1.0.0+b", "v1.0.0-rc.1", "v1.0.0+a"},
		{"v1.0.0+a", "v2.0.0", "v1.0.0-rc.1", "v1.0.0+b"},
	} {
		items := slices.Clone(in)
		virtual.SortVersions(items, func(s string) string { return s }, semver.Compare)
		var builds []string
		for _, v := range in {
			if strings.Contains(v, "+") {
				builds = append(builds, v)
			}
		}
		want := append(append([]string{"v1.0.0-rc.1"}, builds...), "v2.0.0")
		if !slices.Equal(items, want) {
			t.Errorf("sorted %v: %v, want %v", in, items, want)
		}
	}
}

func TestUnitLatest(t *testing.T) {
	stable := func(v string) bool { return semver.Prerelease(v) == "" }
	for _, tc := range []struct {
		versions []string
		want     string
	}{
		{[]string{"v1.0.0", "v3.0.0-alpha", "v2.1.0", "v2.0.0"}, "v2.1.0"},
		{[]string{"v1.0.0-rc.1", "v1.0.0-rc.2", "v0.9.0-beta"}, "v1.0.0-rc.2"},
		{[]string{"v1.0.0"}, "v1.0.0"},
		{nil, ""},
	} {
		if got := virtual.Latest(tc.versions, semver.Compare, stable); got != tc.want {
			t.Errorf("latest of %v: %q, want %q", tc.versions, got, tc.want)
		}
	}
}

// TestUnitAccepts: a merged row needs both the virtual repository's
// patterns and its member's, and a folder passes while an include could
// match below it.
func TestUnitAccepts(t *testing.T) {
	v := virt("virt", "member")
	v.IncludesPattern = new("pkgs/**")
	m := local("member", false)
	m.ExcludesPattern = new("**/*.tmp")
	for _, tc := range []struct {
		path   string
		folder bool
		want   bool
	}{
		{"pkgs/a/1.0/a.tgz", false, true},
		{"other/a.tgz", false, false},
		{"pkgs/a/1.0/a.tmp", false, false},
		{"pkgs", true, true},
		{"other", true, false},
	} {
		if got := virtual.Accepts(v, m, tc.path, tc.folder); got != tc.want {
			t.Errorf("Accepts(%q, folder %v) = %v, want %v", tc.path, tc.folder, got, tc.want)
		}
	}
	if !virtual.Accepts(repo.Config{}, repo.Config{}, "any/path", false) {
		t.Error("repositories without patterns refused a row")
	}
}

func TestUnitBases(t *testing.T) {
	for _, base := range []string{"https://ext.example/root", "https://ext.example/root/"} {
		if got, want := virtual.APIBase(base, "npm", "npm-virtual"),
			"https://ext.example/root/artifactory/api/npm/npm-virtual"; got != want {
			t.Errorf("APIBase(%q) = %s, want %s", base, got, want)
		}
		if got, want := virtual.ItemBase(base, "libs"),
			"https://ext.example/root/artifactory/libs"; got != want {
			t.Errorf("ItemBase(%q) = %s, want %s", base, got, want)
		}
	}
}
