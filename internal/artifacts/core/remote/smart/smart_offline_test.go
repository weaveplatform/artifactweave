package smart

import (
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// TestAccSmartAssumedOfflineAsksThePeerNothing (#1601): while a Smart
// Remote's peer is assumed offline a read makes no discovery, property or
// statistics request and is answered from the cache alone, a cold path
// with the assumed-offline miss, as 7.x asks a remote that isOffline
// nothing.
func TestAccSmartAssumedOfflineAsksThePeerNothing(t *testing.T) {
	w := smartFixture(t)
	opts, err := w.p.Options(w.ctx, w.c)
	if err != nil {
		t.Fatal(err)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	state.ConnectionFailures = 10
	state.OfflineUntil = w.clock.Now().Add(time.Hour)
	state.SettingsRevision = opts.Revision
	if err = w.db.RemoteCache().PutState(w.ctx, w.c.Key, state); err != nil {
		t.Fatal(err)
	}
	if _, err = w.service.Read(w.ctx, w.smartRequest()); !errors.Is(err, source.ErrAssumedOffline) {
		t.Fatalf("%v, want the assumed-offline miss", err)
	}
	if calls := w.peerCalls(); calls != 0 {
		t.Fatalf("%d peer requests while assumed offline", calls)
	}
}
