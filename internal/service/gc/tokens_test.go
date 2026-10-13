package gc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestExpiredTokens: token records go once their refresh grace has run
// out; a token still refreshable and one that never expires stay.
func TestExpiredTokens(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	now := w.now()
	for id, expires := range map[string]time.Time{
		"long-gone":   now.Add(-types.DefaultRefreshGrace - time.Hour),
		"refreshable": now.Add(-time.Hour),
		"live":        now.Add(time.Hour),
		"forever":     {},
	} {
		if err := w.db.Tokens().Create(ctx, types.Token{
			ID:           id,
			Subject:      "jfac@x/users/a",
			IssuedAt:     now.Add(-48 * time.Hour),
			ExpiresAt:    expires,
			AccessDigest: []byte(id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if rep := run(t, w.collector(t, w.db)); rep.TokensExpired != 1 {
		t.Fatalf("report %+v", rep)
	}
	for _, id := range []string{"refreshable", "live", "forever"} {
		if _, err := w.db.Tokens().Get(ctx, id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, err := w.db.Tokens().Get(ctx, "long-gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("long-gone kept: %v", err)
	}
	broken := failingTokens{Store: w.db}
	if _, err := w.collector(t, broken).Run(ctx); err == nil {
		t.Fatal("a failed token reap passed silently")
	}
}

type failingTokens struct{ *inmem.Store }

func (f failingTokens) Tokens() store.TokenStore { return brokenTokens{f.Store.Tokens()} }

type brokenTokens struct{ store.TokenStore }

func (brokenTokens) DeleteExpired(context.Context, time.Time) (int64, error) {
	return 0, errors.New("database down")
}
