package puppethttp_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitHostedListLimitHintsNoRetry: a list past its bound answers 503
// without Retry-After. Decision 0043's "Rate headers and Retry-After" hints
// a retry only on a 503 that reports a transient condition, and a size
// bound is not one (#1415).
func TestUnitHostedListLimitHintsNoRetry(t *testing.T) {
	for _, tc := range []struct {
		route string
		tune  func(*listStore)
	}{
		{"/v3/modules", func(s *listStore) { s.many = true }},
		{"/v3/releases", func(s *listStore) { s.huge = true }},
	} {
		t.Run(tc.route, func(t *testing.T) {
			var s *listStore
			f := newFixtureStore(t, repo.Local, nil,
				func(st pkgroute.Store) pkgroute.Store { s = &listStore{Store: st}; return s })
			f.deployList(f.c)
			tc.tune(s)
			res := f.do("GET", base+tc.route, "admin")
			if res.Code != 503 || res.Header().Get("Retry-After") != "" {
				t.Fatal(res.Code, res.Header(), res.Body)
			}
		})
	}
}
