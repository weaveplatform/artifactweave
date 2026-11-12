package remote

import (
	"net/http"
	"testing"
)

// The remote cache keeps an upstream answer whatever no-store it carries, and
// keeps the upstream's other Cache-Control directives beside it; only an
// uncacheable Vary keeps an answer out.
func TestUnitUpstreamNoStoreIsKept(t *testing.T) {
	for _, tc := range []struct{ in, out string }{
		{"", ""},
		{"no-store", ""},
		{"NO-STORE", ""},
		{"private, no-store", "private"},
		{"no-store, max-age=60", "max-age=60"},
		{"no-cache, no-store, must-revalidate", "no-cache, must-revalidate"},
		{" max-age=5 ,, no-store ", "max-age=5"},
		{"no-store=\"x\"", ""},
	} {
		if got := persistedCacheControl(tc.in); got != tc.out {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.out)
		}
	}
	for _, tc := range []struct {
		header http.Header
		keep   bool
	}{
		{http.Header{"Cache-Control": {"no-store"}}, true},
		{http.Header{"Cache-Control": {"no-store"}, "Vary": {"Accept"}}, true},
		{http.Header{"Vary": {"Cookie"}}, false},
		{http.Header{"Vary": {"*"}}, false},
	} {
		if mustNotPersist(tc.header) == tc.keep {
			t.Errorf("%v: kept %v", tc.header, !tc.keep)
		}
		// The smart remote's documents still honour no-store.
		if MustNotStore(tc.header) != (tc.header.Get("Cache-Control") != "" || !tc.keep) {
			t.Errorf("%v: MustNotStore changed", tc.header)
		}
	}
	// A kept answer's freshness and failure fallback follow the repository,
	// not the upstream's no-store.
	r := parseCacheRules(persistedCacheControl("no-store"))
	if !r.permitsStale() || r.mustNotStore {
		t.Fatal("upstream no-store still read")
	}
}
