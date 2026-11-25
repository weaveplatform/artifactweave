package peer_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
)

// POST /streams/{stream}/resync starts a full sync of the stream from the
// caller, over the signed transport, and its refusals reach the caller as
// the member's refusal (FED-04).
func TestUnitRequestResync(t *testing.T) {
	var asked []string
	var answer error
	f := &fakeStreams{fullSync: func(peerID, stream string, forced bool) (model.FullSyncRun, error) {
		asked = append(asked, fmt.Sprintf("%s %s %t", peerID, stream, forced))
		return model.FullSyncRun{}, answer
	}}
	rm, sr := streamRemote(t, f)
	cr := peeradapter.ConfigRemote{Factory: sr.Factory}
	for _, forced := range []bool{false, true} {
		if err := cr.RequestResync(t.Context(), rm.peer, "repo:fed", forced); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{callerID + " repo:fed false", callerID + " repo:fed true"}; fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	for _, c := range []struct {
		err    error
		status int
	}{
		{streamsync.ErrNoSnapshot, http.StatusNotFound},
		{streamsync.ErrNoStream, http.StatusNotFound},
		{fmt.Errorf("peer: %w", kernel.ErrNotFound), http.StatusNotFound},
		{streamsync.ErrRefused, http.StatusForbidden},
		{kernel.ErrStore, http.StatusInternalServerError},
	} {
		answer = c.err
		err := cr.RequestResync(t.Context(), rm.peer, "repo:fed", false)
		var refusal *federated.RemoteRefusal
		if !errors.As(err, &refusal) || refusal.Status != c.status {
			t.Errorf("%v: %v, want a %d refusal", c.err, err, c.status)
		}
	}
	nokey := rm.peer
	nokey.PublicKey = nil
	if err := cr.RequestResync(t.Context(), nokey, "repo:fed", false); !errors.Is(err, federated.ErrUnavailable) {
		t.Fatalf("no key: %v", err)
	}

	srv := peeradapter.StreamServer{Streams: f}
	if _, err := srv.RequestResync(t.Context(), peergen.RequestResyncRequestObject{Stream: "repo:fed"}); statusOf(t, err) != http.StatusUnauthorized {
		t.Error("unsigned resync")
	}
	signed := corepeer.WithCaller(t.Context(), model.Peer{InstanceID: callerID})
	if _, err := srv.RequestResync(signed, peergen.RequestResyncRequestObject{Stream: "repo:fed"}); statusOf(t, err) != http.StatusBadRequest {
		t.Error("resync without a body")
	}
}
