package storetest

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RunNuGetSearch runs the contract of PackageStore.SearchNuGet on stores
// from newStore: the candidates npm's search returns (RunNpmSearch), only
// those whose names start with a prefix when the search gives one. Each
// adapter's tests call it beside Run.
func RunNuGetSearch(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("NuGetSearchCandidates", func(t *testing.T) {
		s := newStore(t).Packages()
		seedSearch(t, s)
		for _, c := range searchCases {
			got := searchAll(t, func(pg kernel.Page) ([]port.SearchCandidate, string, error) {
				return s.SearchNuGet(t.Context(),
					port.NuGetSearch{RepoKey: "search", Terms: c.terms}, pg)
			}, 2)
			if diff := cmp.Diff(c.want, names(got)); diff != "" {
				t.Errorf("terms %q (-want +got):\n%s", c.terms, diff)
			}
			sameVersions(t, s, got)
		}
	})
	t.Run("NuGetSearchPrefix", func(t *testing.T) {
		s := newStore(t).Packages()
		seedSearch(t, s)
		for _, name := range []string{
			"q", "q~", "q\u007f", "q\ud7ff", "q\ud7ffz", "q\ue000", "q\U0010ffff", "q\U0010ffffz",
			"qu", "r", "\U0010ffff",
		} {
			seedPackage(t, s, "search", name, map[string]string{"1.0.0": `{}`})
		}
		for _, c := range []struct {
			prefix string
			terms  []string
			want   []string
		}{
			{"", []string{"widget"}, []string{"alpha", "beta", "nometa", "widget-kit"}},
			// The names that start with the prefix, in bytes, itself
			// included, and none that sort after them.
			{"q", nil, []string{
				"q", "qu", "quote", "q~", "q\u007f", "q\ud7ff", "q\ud7ffz", "q\ue000",
				"q\U0010ffff", "q\U0010ffffz",
			}},
			{"qu", nil, []string{"qu", "quote"}},
			{"q\u007f", nil, []string{"q\u007f"}},
			// U+D7FF grows past the surrogates, which UTF-8 cannot hold, to
			// U+E000, which it does not start.
			{"q\ud7ff", nil, []string{"q\ud7ff", "q\ud7ffz"}},
			{"q\U0010ffff", nil, []string{"q\U0010ffff", "q\U0010ffffz"}},
			{"\U0010ffff", nil, []string{"\U0010ffff"}},
			{"q", []string{"there"}, []string{"quote"}},
			{"z", nil, []string{}},
			// No name holds a NUL byte.
			{"q\x00", nil, []string{}},
		} {
			got := searchAll(t, func(pg kernel.Page) ([]port.SearchCandidate, string, error) {
				return s.SearchNuGet(t.Context(),
					port.NuGetSearch{RepoKey: "search", Terms: c.terms, Prefix: c.prefix}, pg)
			}, 2)
			if !slices.Equal(c.want, names(got)) {
				t.Errorf("prefix %q, terms %q: %q, want %q", c.prefix, c.terms, names(got), c.want)
			}
		}
	})
}
