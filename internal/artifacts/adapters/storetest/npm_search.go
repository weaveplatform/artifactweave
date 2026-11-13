package storetest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RunNpmSearch runs the contract of PackageStore.SearchNpm on stores from
// newStore: which packages a search's terms make candidates
// (port/npm_search.go), in name order, a page at a time, each with all its
// versions. Each adapter's tests call it beside Run.
func RunNpmSearch(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("NpmSearchCandidates", func(t *testing.T) {
		s := newStore(t).Packages()
		seedSearch(t, s)
		for _, c := range searchCases {
			got := searchAll(t, func(pg kernel.Page) ([]port.SearchCandidate, string, error) {
				return s.SearchNpm(
					t.Context(),
					port.NpmSearch{RepoKey: "search", Terms: c.terms},
					pg,
				)
			}, 2)
			if diff := cmp.Diff(c.want, names(got)); diff != "" {
				t.Errorf("terms %q (-want +got):\n%s", c.terms, diff)
			}
			sameVersions(t, s, got)
		}
	})
	t.Run("NpmSearchPaging", func(t *testing.T) {
		s := newStore(t).Packages()
		seedSearch(t, s)
		ctx := t.Context()
		q := port.NpmSearch{RepoKey: "search"}
		page, next, err := s.SearchNpm(ctx, q, kernel.Page{Limit: 3})
		if err != nil || !slices.Equal(names(page), []string{"alpha", "beta", "city"}) ||
			next == "" {
			t.Fatalf("first page %v, %q, %v", names(page), next, err)
		}
		page, next, err = s.SearchNpm(ctx, q, kernel.Page{After: next, Limit: 3})
		if err != nil || !slices.Equal(names(page), []string{"gamma", "nometa", "pct"}) ||
			next == "" {
			t.Fatalf("second page %v, %q, %v", names(page), next, err)
		}
		// The cursor is List's: the name the last page ended at.
		page, next, err = s.SearchNpm(ctx, q, kernel.Page{After: kernel.Cursor("quote"), Limit: 9})
		if err != nil || !slices.Equal(names(page), []string{"temp", "widget-kit"}) || next != "" {
			t.Fatalf("last page %v, %q, %v", names(page), next, err)
		}
		_, _, err = s.SearchNpm(ctx, q, kernel.Page{After: "%%%"})
		if !errors.Is(err, kernel.ErrInvalidCursor) {
			t.Errorf("bad cursor: %v", err)
		}
		// A query no store could take whole (very many or very long terms)
		// is narrowed by part of it, never refused.
		many := slices.Repeat([]string{"a"}, 40)
		long := []string{strings.Repeat("widget", 2000)}
		for _, terms := range [][]string{many, long} {
			if _, _, err := s.SearchNpm(ctx, port.NpmSearch{RepoKey: "search", Terms: terms},
				kernel.Page{}); err != nil {
				t.Errorf("%d terms of %d bytes: %v", len(terms), len(terms[0]), err)
			}
		}
		empty, next, err := s.SearchNpm(ctx, port.NpmSearch{RepoKey: "absent"}, kernel.Page{})
		if err != nil || len(empty) != 0 || next != "" {
			t.Errorf("absent repository: %v, %q, %v", empty, next, err)
		}
	})
}

// searchCase is a search's terms and the candidates it must return.
type searchCase struct {
	terms []string
	want  []string
}

// Every search returns beta, whose metadata holds a \u escape, and nometa,
// whose version has none; noversion, with no version, never.
var searchCases = []searchCase{
	{
		nil,
		[]string{"alpha", "beta", "city", "gamma", "nometa", "pct", "quote", "temp", "widget-kit"},
	},
	// In the name (widget-kit) or a version's metadata, ignoring ASCII
	// case (alpha 1.0.0's "Widget").
	{[]string{"widget"}, []string{"alpha", "beta", "nometa", "widget-kit"}},
	{[]string{"wid", "get"}, []string{"alpha", "beta", "nometa", "widget-kit"}},
	// Every term in the name or in the same version: alpha's 2.0.0 says
	// second and its name alpha, but no version says both second and
	// widget.
	{[]string{"second", "alpha"}, []string{"alpha", "beta", "nometa"}},
	{[]string{"second", "widget"}, []string{"beta", "nometa"}},
	// The two runes strings.ToLower makes ASCII: U+212A and U+0130.
	{[]string{"kelvin"}, []string{"beta", "nometa", "temp"}},
	{[]string{"istanbul"}, []string{"beta", "city", "nometa"}},
	// % and _ are themselves, not wildcards.
	{[]string{"100%_off"}, []string{"beta", "nometa", "pct"}},
	{[]string{"1_0"}, []string{"beta", "nometa"}},
	{[]string{"0%_"}, []string{"beta", "nometa", "pct"}},
	// Quotes, backslashes and slashes end a run: JSON may escape them.
	{[]string{`"hi/there"`}, []string{"beta", "nometa", "quote"}},
	{[]string{`say\`}, []string{"beta", "nometa", "quote"}},
	// A term without printable ASCII narrows nothing.
	{
		[]string{"é"},
		[]string{"alpha", "beta", "city", "gamma", "nometa", "pct", "quote", "temp", "widget-kit"},
	},
	{[]string{"absent"}, []string{"beta", "nometa"}},
}

// seedSearch writes the packages of the repository "search", and one of
// another repository that no search of "search" returns.
func seedSearch(t *testing.T, s port.PackageStore) {
	t.Helper()
	for _, p := range []struct {
		name     string
		versions map[string]string
	}{
		{"alpha", map[string]string{
			"1.0.0": `{"manifest":{"description":"Alpha Widget"}}`,
			"2.0.0": `{"manifest":{"description":"second"}}`,
		}},
		{"beta", map[string]string{"1.0.0": `{"manifest":{"description":"\u0057idget"}}`}},
		{"city", map[string]string{"1.0.0": `{"manifest":{"description":"İSTANBUL"}}`}},
		{"gamma", map[string]string{"1.0.0": `{"manifest":{"description":"plain"}}`}},
		{"nometa", map[string]string{"1.0.0": ``}},
		{"noversion", nil},
		{"pct", map[string]string{"1.0.0": `{"manifest":{"description":"100%_OFF"}}`}},
		{"quote", map[string]string{"1.0.0": `{"manifest":{"description":"say \"HI/there\" \\o/"}}`}},
		{"temp", map[string]string{"1.0.0": "{\"manifest\":{\"description\":\"\u212aELVIN\"}}"}},
		{"widget-kit", map[string]string{"1.0.0": `{"manifest":{}}`}},
	} {
		seedPackage(t, s, "search", p.name, p.versions)
	}
	seedPackage(t, s, "other", "alpha", map[string]string{"1.0.0": `{}`})
}

// seedPackage writes the package name of repoKey with versions, each
// version's stored metadata by its number.
func seedPackage(
	t *testing.T,
	s port.PackageStore,
	repoKey, name string,
	versions map[string]string,
) {
	t.Helper()
	ctx := context.Background()
	pkg, err := s.Ensure(ctx, model.Package{RepoKey: repoKey, Type: repo.NPM, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	for v, meta := range versions {
		if _, err := s.PutVersion(ctx, model.PackageVersion{
			PackageID: pkg.ID, Version: v, Metadata: []byte(meta),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// searchAll pages through a search limit candidates at a time.
func searchAll(
	t *testing.T,
	search func(kernel.Page) ([]port.SearchCandidate, string, error),
	limit int,
) []port.SearchCandidate {
	t.Helper()
	var out []port.SearchCandidate
	pg := kernel.Page{Limit: limit}
	for {
		page, next, err := search(pg)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, page...)
		if next == "" {
			return out
		}
		pg.After = next
	}
}

// names are the candidates' package names, in their order.
func names(cs []port.SearchCandidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Package.Name)
	}
	return out
}

// sameVersions checks that each candidate comes with the versions
// Versions returns for it, in that order, and as Find returns the package.
func sameVersions(t *testing.T, s port.PackageStore, cs []port.SearchCandidate) {
	t.Helper()
	ctx := context.Background()
	for _, c := range cs {
		want, err := s.Versions(ctx, c.Package.ID)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, c.Versions); diff != "" {
			t.Errorf("%s's versions (-want +got):\n%s", c.Package.Name, diff)
		}
		pkg, err := s.Find(ctx, c.Package.RepoKey, c.Package.Name)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(pkg, c.Package); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.Package.Name, diff)
		}
	}
}
