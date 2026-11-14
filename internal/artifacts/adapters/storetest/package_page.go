package storetest

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunPackagePages runs the contracts of the store's list paging
// (port.PackagePages, and PackageVersionNames' CountVersions and
// VersionNamesAt) on stores from newStore: Puppet's, Chef's and Ansible's
// versions, written in random order, read back newest first in their
// lists' order, and a repository's packages are counted, paged, tallied and
// listed by release as those lists and npm's and NuGet's searches count
// them. The adapters' tests call it beside Run.
func RunPackagePages(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("ListOrderKeys", func(t *testing.T) { listOrderKeys(t, newStore) })
	t.Run("PackagePages", func(t *testing.T) { packagePages(t, newStore) })
	t.Run("NuGetShown", func(t *testing.T) { nuGetShown(t, newStore) })
	t.Run("TallyAndReleases", func(t *testing.T) { tallyAndReleases(t, newStore) })
}

func listOrderKeys(t *testing.T, newStore Factory) {
	ctx := t.Context()
	s := newStore(t).Packages()
	rnd := rand.New(rand.NewPCG(1426, 1)) //nolint:gosec // test data, not a secret
	names := storekit.Get[[]string](t)
	for _, c := range []struct {
		pt repo.PackageType
		// newestFirst is the list's order: x/mod/semver's, highest first,
		// invalid versions last; ties in byte order for Puppet and Chef,
		// and in Maven's order for Ansible.
		newestFirst []string
	}{
		{repo.Puppet, []string{
			"2.0.0-rc.1", "1.10.0", "1.9.0", "1.2", "1.0.0", "1.0.0+a", "1.0.0+b", "1.0.0-1",
			"bogus", "latest",
		}},
		{repo.Chef, []string{"10.0", "1.10", "1.2", "1.2.0", "0.9.1"}},
		{repo.Ansible, []string{
			"2.0.0-rc.1", "1.10.0", "1.0.0", "1.0.0+2", "1.0.0+10", "1.0.0+a", "1.0.0+b",
			"1.0.0-rc.1",
		}},
	} {
		pkg := storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
			RepoKey: string(c.pt) + "-local", Type: c.pt, Name: "acme-widget",
		}))
		for _, i := range rnd.Perm(len(c.newestFirst)) {
			storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, model.PackageVersion{
				PackageID: pkg.ID, Version: c.newestFirst[i], Metadata: []byte(`{}`),
			}))
		}
		oldestFirst := slices.Clone(c.newestFirst)
		slices.Reverse(oldestFirst)
		what := string(c.pt) + ": "
		storekit.Same(t, what+"VersionNames", oldestFirst, names(s.VersionNames(ctx, pkg.ID)))
		storekit.Same(t, what+"CountVersions", len(c.newestFirst),
			storekit.Get[int](t)(s.CountVersions(ctx, pkg.ID)))
		storekit.Same(t, what+"newest first", c.newestFirst,
			names(s.VersionNamesAt(ctx, pkg.ID, true, 0, 100)))
		storekit.Same(t, what+"a page newest first", c.newestFirst[2:4],
			names(s.VersionNamesAt(ctx, pkg.ID, true, 2, 2)))
		storekit.Same(t, what+"a page oldest first", oldestFirst[1:4],
			names(s.VersionNamesAt(ctx, pkg.ID, false, 1, 3)))
		storekit.Same(t, what+"the last page", c.newestFirst[len(c.newestFirst)-1:],
			names(s.VersionNamesAt(ctx, pkg.ID, true, len(c.newestFirst)-1, 5)))
		for _, empty := range [][2]int{{len(c.newestFirst), 5}, {0, 0}, {1 << 40, 1}} {
			if got := names(
				s.VersionNamesAt(ctx, pkg.ID, true, empty[0], empty[1]),
			); len(
				got,
			) != 0 {
				t.Errorf("%soffset %d limit %d: %q", what, empty[0], empty[1], got)
			}
		}
		storekit.Same(t, what+"a negative offset reads from the start", c.newestFirst[:1],
			names(s.VersionNamesAt(ctx, pkg.ID, true, -1, 1)))
	}
	storekit.Same(t, "no package's count", 0, storekit.Get[int](t)(s.CountVersions(ctx, 1<<40)))
}

// seedPages writes repository "pages" for the package range contracts:
// packages a to f, with "e" holding no version, and a package of another
// repository between them.
func seedPages(t *testing.T, s port.PackageStore) map[string]model.Package {
	t.Helper()
	ctx := t.Context()
	out := map[string]model.Package{}
	for _, name := range []string{"f", "b", "e", "a", "d", "c"} {
		pkg := storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
			RepoKey: "pages", Type: repo.NPM, Name: name,
		}))
		out[name] = pkg
		if name == "e" {
			continue
		}
		for _, v := range []string{"1.0.0", "2.0.0"} {
			storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, model.PackageVersion{
				PackageID: pkg.ID, Version: v, Metadata: []byte(`{"name":"` + name + `"}`),
			}))
		}
	}
	other := storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
		RepoKey: "pages-other", Type: repo.NPM, Name: "c2",
	}))
	storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, model.PackageVersion{
		PackageID: other.ID, Version: "1.0.0",
	}))
	return out
}

func packageNames(ps []model.Package) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func packagePages(t *testing.T, newStore Factory) {
	ctx := t.Context()
	s := newStore(t).Packages()
	seeded := seedPages(t, s)
	count := storekit.Get[int](t)
	page := storekit.Get[[]model.Package](t)
	all := port.PackageRange{RepoKey: "pages"}
	storekit.Same(t, "packages with a version", 5, count(s.CountPackages(ctx, all)))
	storekit.Same(t, "every page", []string{"a", "b", "c", "d", "f"},
		packageNames(page(s.PackagesAt(ctx, all, 0, 10))))
	storekit.Same(t, "a late page", []string{"d", "f"},
		packageNames(page(s.PackagesAt(ctx, all, 3, 2))))
	storekit.Same(t, "a negative offset", []string{"a"},
		packageNames(page(s.PackagesAt(ctx, all, -3, 1))))
	if got := page(s.PackagesAt(ctx, all, 5, 2)); len(got) != 0 {
		t.Errorf("past the end: %q", packageNames(got))
	}
	if got := page(s.PackagesAt(ctx, all, 0, 0)); len(got) != 0 {
		t.Errorf("a page of none: %q", packageNames(got))
	}
	storekit.Same(t, "a page's records", seeded["b"], page(s.PackagesAt(ctx, all, 1, 1))[0])
	prefix := port.PackageRange{RepoKey: "pages", Prefix: "c"}
	storekit.Same(t, "a prefix", 1, count(s.CountPackages(ctx, prefix)))
	storekit.Same(t, "a prefix's page", []string{"c"},
		packageNames(page(s.PackagesAt(ctx, prefix, 0, 5))))
	for _, none := range []port.PackageRange{
		{RepoKey: "pages", Prefix: "c\x00"}, {RepoKey: "pages", Prefix: "e"}, {RepoKey: "absent"},
	} {
		if n := count(s.CountPackages(ctx, none)); n != 0 {
			t.Errorf("%+v counts %d", none, n)
		}
		if got := page(s.PackagesAt(ctx, none, 0, 5)); len(got) != 0 {
			t.Errorf("%+v pages %q", none, packageNames(got))
		}
	}
	// Candidates keeps the order it is given and drops a package with no
	// version, as a package deleted since its page was read has none.
	got := storekit.Get[[]port.SearchCandidate](t)(s.Candidates(ctx,
		[]model.Package{seeded["d"], seeded["e"], seeded["a"]}))
	storekit.Same(t, "candidates", []string{"d", "a"}, names(got))
	sameVersions(t, s, got)
	if got := storekit.Get[[]port.SearchCandidate](t)(s.Candidates(ctx, nil)); len(got) != 0 {
		t.Errorf("no candidates: %v", names(got))
	}
}

func nuGetShown(t *testing.T, newStore Factory) {
	ctx := t.Context()
	s := newStore(t).Packages()
	count := storekit.Get[int](t)
	// Each package's one version is the kind its name says; "mixed" holds
	// a pre-release and a SemVer 2.0.0 version, and "two" a symbols-only
	// version beside a release.
	for name, versions := range map[string][]string{
		"release":    {`{"path":"r.nupkg","version":"1.0.0"}`},
		"prerelease": {`{"path":"p.nupkg","version":"1.0.0-Beta"}`},
		"semver2":    {`{"path":"s.nupkg","version":"1.0.0","semver2":true}`},
		"symbols":    {`{"version":"1.0.0"}`, `{"path":"","version":"2.0.0"}`},
		"nometa":     {""},
		"mixed":      {`{"path":"m.nupkg","version":"1.0.0-rc"}`, `{"path":"m2.nupkg","version":"2.0.0","semver2":true}`},
		"two":        {`{"version":"1.0.0"}`, `{"path":"t.nupkg","version":"2.0.0","semver2":false}`},
		"nulls":      {`{"path":"n.nupkg","version":null,"semver2":null}`},
	} {
		pkg := storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
			RepoKey: "nuget", Type: repo.NuGet, Name: name,
		}))
		for i, m := range versions {
			v := model.PackageVersion{PackageID: pkg.ID, Version: string(rune('1'+i)) + ".0.0"}
			if m != "" {
				v.Metadata = []byte(m)
			}
			storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, v))
		}
	}
	for _, c := range []struct {
		shown port.NuGetShown
		want  []string
	}{
		{port.NuGetShown{}, []string{"nulls", "release", "two"}},
		{port.NuGetShown{Prerelease: true}, []string{"mixed", "nulls", "prerelease", "release", "two"}},
		{port.NuGetShown{SemVer2: true}, []string{"mixed", "nulls", "release", "semver2", "two"}},
		{
			port.NuGetShown{Prerelease: true, SemVer2: true},
			[]string{"mixed", "nulls", "prerelease", "release", "semver2", "two"},
		},
	} {
		r := port.PackageRange{RepoKey: "nuget", Shown: &c.shown}
		storekit.Same(t, "count shown", len(c.want), count(s.CountPackages(ctx, r)))
		storekit.Same(t, "page shown", c.want,
			packageNames(storekit.Get[[]model.Package](t)(s.PackagesAt(ctx, r, 0, 20))))
	}
	// Metadata that is not JSON fails a NuGet range's read.
	pkg := storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
		RepoKey: "nuget-bad", Type: repo.NuGet, Name: "bad",
	}))
	storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, model.PackageVersion{
		PackageID: pkg.ID, Version: "1.0.0", Metadata: []byte(`{not json`),
	}))
	bad := port.PackageRange{RepoKey: "nuget-bad", Shown: &port.NuGetShown{}}
	if _, err := s.CountPackages(ctx, bad); err == nil {
		t.Error("a count over metadata that is not JSON did not fail")
	}
	if _, err := s.PackagesAt(ctx, bad, 0, 1); err == nil {
		t.Error("a page over metadata that is not JSON did not fail")
	}
	storekit.Same(t, "without Shown the metadata is not read", 1,
		count(s.CountPackages(ctx, port.PackageRange{RepoKey: "nuget-bad"})))
}

func tallyAndReleases(t *testing.T, newStore Factory) {
	ctx := t.Context()
	s := newStore(t).Packages()
	tally := storekit.Get[port.VersionTally](t)
	// Puppet modules, so each one's versions read back newest first; "c"
	// holds none.
	for name, versions := range map[string][]string{
		"a": {"1.0.0", "1.10.0", "1.9.0"},
		"b": {"0.1.0"},
		"c": nil,
		"d": {"2.0.0", "2.0.0-rc.1", "1.0.0", "3.0.0"},
	} {
		pkg := storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
			RepoKey: "forge", Type: repo.Puppet, Name: name,
		}))
		for _, v := range versions {
			storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, model.PackageVersion{
				PackageID: pkg.ID, Version: v,
			}))
		}
	}
	storekit.Same(t, "every package", port.VersionTally{Packages: 4, Versions: 8, MostVersions: 4},
		tally(s.TallyVersions(ctx, "forge", 10)))
	storekit.Same(t, "the first three by name",
		port.VersionTally{Packages: 3, Versions: 4, MostVersions: 3},
		tally(s.TallyVersions(ctx, "forge", 3)))
	storekit.Same(t, "none", port.VersionTally{}, tally(s.TallyVersions(ctx, "forge", 0)))
	storekit.Same(t, "an empty repository", port.VersionTally{},
		tally(s.TallyVersions(ctx, "absent", 10)))
	releases := func(offset, limit int) []string {
		t.Helper()
		page := storekit.Get[[]port.ReleaseName](t)(s.ReleasesAt(ctx, "forge", offset, limit))
		out := make([]string, 0, len(page))
		for _, r := range page {
			out = append(out, r.Package+"@"+r.Version)
		}
		return out
	}
	every := []string{
		"a@1.10.0", "a@1.9.0", "a@1.0.0", "b@0.1.0",
		"d@3.0.0", "d@2.0.0", "d@2.0.0-rc.1", "d@1.0.0",
	}
	storekit.Same(t, "every release", every, releases(0, 20))
	storekit.Same(t, "a page across modules", every[2:6], releases(2, 4))
	storekit.Same(t, "the last page", every[7:], releases(7, 4))
	if got := releases(8, 4); len(got) != 0 {
		t.Errorf("past the end: %q", got)
	}
	if got := releases(0, 0); len(got) != 0 {
		t.Errorf("a page of none: %q", got)
	}
	first := storekit.Get[[]port.ReleaseName](t)(s.ReleasesAt(ctx, "forge", 0, 1))[0]
	a := storekit.Get[model.Package](t)(s.Find(ctx, "forge", "a"))
	storekit.Same(t, "a release's package", port.ReleaseName{
		PackageID: a.ID, Package: "a", Version: "1.10.0",
	}, first)
}
