package peer_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

// TestUnitStatusOverSignedTransport reads a paired peer's status through its
// signed peer API: its identity, versions, clock and the use cases the
// caller holds there.
func TestUnitStatusOverSignedTransport(t *testing.T) {
	a, b := newNode(t), newNode(t)
	connect(t, a, b)
	tok, err := a.svc.Issue(t.Context(), model.UseFederatedRepo, a.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.srv.URL); err != nil {
		t.Fatal(err)
	}
	remote := peeradapter.StatusRemote{Factory: b.remote.Factory}
	recordOfA := b.peers(t)[0]
	before := time.Now()
	st, err := remote.Status(t.Context(), recordOfA)
	if err != nil {
		t.Fatal(err)
	}
	if st.InstanceID != a.id(t).InstanceID || st.ProtocolVersion != "1.0" || st.Version != "1.2.3" ||
		st.Time.Before(before.Add(-time.Second)) || !slices.Equal(st.UseCases, []model.UseCase{model.UseFederatedRepo}) {
		t.Fatalf("status %+v", st)
	}

	// The probe records it: B sees A online, with no skew to speak of.
	b.svc.Prober = remote
	if err := b.svc.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p := b.peers(t)[0]; p.Status != model.PeerOnline || p.ClockSkew > time.Second || p.ClockSkew < -time.Second {
		t.Fatalf("B's record of A after a read: %+v", p)
	}

	// A key that is not A's: A's signed answer does not verify.
	other := newNode(t)
	forged := recordOfA
	forged.PublicKey = other.id(t).PublicKey
	if _, err := remote.Status(t.Context(), forged); !errors.Is(err, pairing.ErrUnverified) {
		t.Fatalf("an answer the recorded key does not verify: %v", err)
	}
	// A no longer holds B: its refusal is unsigned, and the trust is gone.
	if err := a.svc.Drop(t.Context(), b.id(t).InstanceID, model.UseFederatedRepo); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Status(t.Context(), recordOfA); !errors.Is(err, pairing.ErrUnverified) {
		t.Fatalf("a peer that refuses this instance: %v", err)
	}
}

func TestUnitStatusRemoteErrors(t *testing.T) {
	b := newNode(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(gateway.Close)
	failing := newStatusNode(t, b, failingStatus{})
	connect(t, b, &instanceNode{srv: closed}, &instanceNode{srv: gateway}, &instanceNode{srv: failing.srv})
	remote := peeradapter.StatusRemote{Factory: b.remote.Factory}
	var status *pairing.StatusError
	if _, err := remote.Status(t.Context(), model.Peer{BaseURL: closed.URL, PublicKey: b.id(t).PublicKey}); !errors.Is(err, pairing.ErrUnreachable) {
		t.Fatalf("closed listener: %v", err)
	}
	if _, err := remote.Status(t.Context(), model.Peer{BaseURL: "ftp://peer.example"}); !errors.Is(err, pairing.ErrUnreachable) {
		t.Fatalf("unusable base URL: %v", err)
	}
	// An intermediary's error is only its status.
	if _, err := remote.Status(t.Context(), model.Peer{BaseURL: gateway.URL, PublicKey: b.id(t).PublicKey}); !errors.As(err, &status) || status.Status != 502 {
		t.Fatalf("an intermediary's 502: %v", err)
	}
	// A verified answer that is not 200.
	peer := model.Peer{InstanceID: failing.id.InstanceID, BaseURL: failing.srv.URL, PublicKey: failing.id.PublicKey}
	if _, err := remote.Status(t.Context(), peer); !errors.As(err, &status) || status.Status != 500 {
		t.Fatalf("a signed 500: %v", err)
	}
}

func TestUnitStatusServerRefusals(t *testing.T) {
	s := peeradapter.StatusServer{Status: failingStatus{}}
	if _, err := s.GetPeerStatus(t.Context(), peergen.GetPeerStatusRequestObject{}); statusOf(t, err) != 401 {
		t.Errorf("no verified caller: %v", err)
	}
	ctx := corepeer.WithCaller(t.Context(), model.Peer{InstanceID: "jfrt@b"})
	if _, err := s.GetPeerStatus(ctx, peergen.GetPeerStatusRequestObject{}); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("a failing status: %v", err)
	}
}

// failingStatus is a status service whose store fails.
type failingStatus struct{}

func (failingStatus) Describe(context.Context, model.Peer, string) (pairing.PeerStatus, error) {
	return pairing.PeerStatus{}, kernel.ErrStore
}

// statusNode is a peer API whose /status is served by svc, with caller
// recorded as its peer.
type statusNode struct {
	srv *httptest.Server
	id  instance.InstanceIdentity
}

func newStatusNode(t *testing.T, caller *instanceNode, svc peeradapter.StatusService) *statusNode {
	t.Helper()
	n := newNode(t)
	callerID := caller.id(t)
	if err := n.store.Peers().Put(t.Context(), model.Peer{
		InstanceID: callerID.InstanceID, BaseURL: caller.srv.URL, PublicKey: callerID.PublicKey,
		ProtocolVersion: instance.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use((&peeradapter.Server{
		Verifier: &corepeer.Verifier{Keys: n.store.Peers(), Nonces: n.store.RequestNonces()},
		Signer:   n.signer,
	}).Middleware)
	opts := peergen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	peergen.HandlerWithOptions(
		peergen.NewStrictHandlerWithOptions(server{status: peeradapter.StatusServer{Status: svc}}, nil, opts),
		peergen.ChiServerOptions{
			BaseURL: peeradapter.Prefix, BaseRouter: r,
			ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
		},
	)
	out := &statusNode{srv: httptest.NewServer(r), id: n.id(t)}
	t.Cleanup(out.srv.Close)
	return out
}
