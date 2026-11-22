package replication_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// A pull source or proxy that cannot be checked is a refusal, never an
// accepted target whose URL or proxy was not validated.
func TestUnitConfigurationRefusesUncheckableSourcesAndProxies(t *testing.T) {
	boom := errors.New("dependency unavailable")
	for _, tc := range []struct {
		name, repository string
		input            replication.Input
		change           func(*world)
		want             error
	}{
		{"pull without repository configuration", "remote", replication.Input{}, func(w *world) { w.service.Repositories = nil }, kernel.ErrStore},
		{"pull source read failure", "remote", replication.Input{}, func(w *world) { w.repos.secretError = boom }, boom},
		{
			"proxy without lookup", "local",
			replication.Input{URL: ptr("https://target.example/repo"), Proxy: ptr("corp")},
			func(w *world) { w.service.ProxyExists = nil }, kernel.ErrStore,
		},
		{
			"proxy lookup failure", "local",
			replication.Input{URL: ptr("https://target.example/repo"), Proxy: ptr("corp")},
			func(w *world) { w.service.ProxyExists = func(context.Context, string) error { return boom } }, boom,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			tc.change(w)
			if err := w.service.Replace(t.Context(), tc.repository, tc.input); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			rows, err := w.db.Replications().List(t.Context())
			must(t, err)
			if len(rows) != 0 {
				t.Fatal("an unchecked configuration was published")
			}
		})
	}
}
