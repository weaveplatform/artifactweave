package peer_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// store is one instance's metadata over one in-memory database.
type store struct {
	*fedinmem.Store
	platform *platforminmem.Store
}

func (s store) Outbox() outbox.OutboxStore       { return s.platform.Outbox() }
func (s store) Instance() instance.InstanceStore { return s.platform.Instance() }

// server is the peer API with /pair served, as the application mounts it.
type server struct {
	peergen.UnimplementedStrict
	pair peeradapter.PairServer
}

func (s server) RedeemPairing(
	ctx context.Context, req peergen.RedeemPairingRequestObject,
) (peergen.RedeemPairingResponseObject, error) {
	return s.pair.RedeemPairing(ctx, req)
}

func (s server) CancelPairing(
	ctx context.Context, req peergen.CancelPairingRequestObject,
) (peergen.CancelPairingResponseObject, error) {
	return s.pair.CancelPairing(ctx, req)
}

// instanceNode is one instance: its store, its pairing service over the
// signed peer client, and its peer API on a test listener.
type instanceNode struct {
	store  store
	svc    *pairing.Service
	remote peeradapter.PairRemote
	srv    *httptest.Server
	signer func(context.Context) (corepeer.Signer, error)
}

func (n *instanceNode) id(t *testing.T) instance.InstanceIdentity {
	t.Helper()
	id, err := n.store.Instance().Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (n *instanceNode) peers(t *testing.T) []model.Peer {
	t.Helper()
	ps, err := n.store.Peers().List(t.Context(), port.PeerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func newNode(t *testing.T) *instanceNode {
	t.Helper()
	shared := memtx.New()
	st := store{Store: fedinmem.New(shared), platform: platforminmem.New(shared)}
	if err := instance.EnsureInstance(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	n := &instanceNode{store: st}
	n.svc = &pairing.Service{Store: st, Authorize: func(context.Context) error { return nil }}
	signer := func(ctx context.Context) (corepeer.Signer, error) {
		id, err := st.Instance().Get(ctx)
		return corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey}, err
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use((&peeradapter.Server{
		Verifier: &corepeer.Verifier{Keys: st.Peers(), Nonces: st.RequestNonces()},
		Signer:   signer,
	}).Middleware)
	opts := peergen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
		ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
	}
	peergen.HandlerWithOptions(
		peergen.NewStrictHandlerWithOptions(server{pair: peeradapter.PairServer{Pairing: n.svc}}, nil, opts),
		peergen.ChiServerOptions{
			BaseURL: peeradapter.Prefix, BaseRouter: r,
			ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest),
		},
	)
	n.srv = httptest.NewServer(r)
	t.Cleanup(n.srv.Close)
	n.signer = signer
	return n
}

// connect lets each node call the others, whose listeners are on private
// addresses the client refuses unless granted.
func connect(t *testing.T, nodes ...*instanceNode) {
	t.Helper()
	var origins []string
	for _, n := range nodes {
		origins = append(origins, n.srv.URL)
	}
	for _, n := range nodes {
		if n.svc == nil {
			continue
		}
		f, err := peeradapter.NewFactory(peeradapter.ClientOptions{
			Signer: n.signer, AllowedPrivateOrigins: origins, Attempts: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		n.remote = peeradapter.PairRemote{Factory: f}
		n.svc.Remote = n.remote
	}
}

// TestUnitPairOverSignedTransport pairs two instances through their peer
// APIs, each request and answer signed, then unpairs them.
func TestUnitPairOverSignedTransport(t *testing.T) {
	a, b, c := newNode(t), newNode(t), newNode(t)
	connect(t, a, b, c)
	tok, err := a.svc.Issue(t.Context(), model.UseFederatedRepo, a.srv.URL+"/artifactory")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.srv.URL+"/artifactory"); err != nil {
		t.Fatal(err)
	}
	ap, bp := a.peers(t), b.peers(t)
	if len(ap) != 1 || ap[0].InstanceID != b.id(t).InstanceID || ap[0].BaseURL != b.srv.URL ||
		!ap[0].PublicKey.Equal(b.id(t).PublicKey) {
		t.Fatalf("issuer's peers %+v", ap)
	}
	if len(bp) != 1 || bp[0].InstanceID != a.id(t).InstanceID || bp[0].BaseURL != a.srv.URL ||
		!bp[0].PublicKey.Equal(a.id(t).PublicKey) {
		t.Fatalf("redeemer's peers %+v", bp)
	}

	// The token's nonce is spent: a second redeemer is refused by the
	// issuer with 403, which the redeemer reports as 7.x's status reason.
	err = c.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, c.srv.URL)
	var refusal *pairing.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != "Error exchanging token: unacceptable http status code from target server 403" {
		t.Fatalf("reused token: %v", err)
	}
	if len(c.peers(t)) != 0 || len(a.peers(t)) != 1 {
		t.Fatal("a refused redeem left a peer record")
	}

	if err := b.svc.Unpair(t.Context(), model.UseFederatedRepo, a.srv.URL+"/artifactory/", false); err != nil {
		t.Fatal(err)
	}
	if len(a.peers(t)) != 0 || len(b.peers(t)) != 0 {
		t.Fatalf("unpair left peers: %+v %+v", a.peers(t), b.peers(t))
	}
}

func TestUnitPairRemoteErrors(t *testing.T) {
	a, b := newNode(t), newNode(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	forged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance_id":"jfrt@x","base_url":"x","public_key":"` +
			base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)) +
			`","protocol_version":"1.0","use_case":"federated-repo/*"}`))
	}))
	t.Cleanup(forged.Close)
	connect(t, a, b, &instanceNode{srv: closed}, &instanceNode{srv: forged})
	id := b.id(t)
	req := pairing.Request{
		Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), InstanceID: id.InstanceID,
		BaseURL: b.srv.URL, PublicKey: id.PublicKey, ProtocolVersion: "1.0",
	}
	// An unknown nonce: the issuer's 403 carries no identity to verify it
	// with, so only its status comes back.
	var status *pairing.StatusError
	if _, err := b.remote.Pair(t.Context(), a.srv.URL, req); !errors.As(err, &status) || status.Status != 403 {
		t.Fatalf("unknown nonce: %v", err)
	}
	if _, err := b.remote.Pair(t.Context(), closed.URL, req); !errors.Is(err, pairing.ErrUnreachable) {
		t.Fatalf("closed listener: %v", err)
	}
	if _, err := b.remote.Pair(t.Context(), "ftp://peer.example", req); !errors.Is(err, pairing.ErrUnreachable) {
		t.Fatalf("unusable base URL: %v", err)
	}
	// A 200 that is not the issuer's own: an intermediary answering with a
	// body that names a key it did not sign with.
	if _, err := b.remote.Pair(t.Context(), forged.URL, req); !errors.Is(err, pairing.ErrUnverified) {
		t.Fatalf("forged answer: %v", err)
	}

	// Unpair from an instance that holds no record of the caller: its 401
	// is not signed, so the caller cannot tell it from a forgery.
	peerA := model.Peer{InstanceID: a.id(t).InstanceID, BaseURL: a.srv.URL, PublicKey: a.id(t).PublicKey}
	if err := b.remote.Unpair(t.Context(), peerA, model.UseFederatedRepo); !errors.Is(err, pairing.ErrUnverified) {
		t.Fatalf("unpair unknown caller: %v", err)
	}
	if err := b.remote.Unpair(t.Context(), model.Peer{BaseURL: "ftp://peer.example"}, model.UseFederatedRepo); !errors.Is(err, pairing.ErrUnreachable) {
		t.Fatalf("unpair unusable base URL: %v", err)
	}
	// Once paired, a use case /pair does not serve is a signed 400.
	tok, err := a.svc.Issue(t.Context(), model.UseFederatedRepoRead, a.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.svc.Redeem(t.Context(), model.UseFederatedRepoRead, tok, b.srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := b.remote.Unpair(t.Context(), peerA, model.UseDistribution); !errors.As(err, &status) || status.Status != 400 {
		t.Fatalf("unserved use case: %v", err)
	}
}

// fakePairing is the issuing side's service with fixed answers.
type fakePairing struct {
	err     error
	dropped []string
}

func (f *fakePairing) Accept(context.Context, pairing.Request, string) (pairing.Answer, error) {
	return pairing.Answer{}, f.err
}

func (f *fakePairing) Drop(_ context.Context, id string, use model.UseCase) error {
	f.dropped = append(f.dropped, id+" "+string(use))
	return f.err
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var e *apierror.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not an API error", err)
	}
	return e.Status
}

func TestUnitPairServerRefusals(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(pub)
	caller := model.Peer{InstanceID: "jfrt@b", PublicKey: pub}
	signed := corepeer.WithCaller(t.Context(), caller)
	body := &peergen.RedeemPairingJSONRequestBody{InstanceId: "jfrt@b", PublicKey: key}
	f := &fakePairing{}
	s := peeradapter.PairServer{Pairing: f}

	redeem := func(ctx context.Context, b *peergen.RedeemPairingJSONRequestBody) error {
		_, err := s.RedeemPairing(ctx, peergen.RedeemPairingRequestObject{Body: b})
		return err
	}
	if got := statusOf(t, redeem(signed, nil)); got != 400 {
		t.Errorf("no body: %d", got)
	}
	if got := statusOf(t, redeem(signed, &peergen.RedeemPairingJSONRequestBody{PublicKey: "short"})); got != 400 {
		t.Errorf("bad key: %d", got)
	}
	if got := statusOf(t, redeem(t.Context(), body)); got != 401 {
		t.Errorf("no verified caller: %d", got)
	}
	other := *body
	other.InstanceId = "jfrt@c"
	if got := statusOf(t, redeem(signed, &other)); got != 401 {
		t.Errorf("another instance's body: %d", got)
	}
	f.err = pairing.ErrInvalidRequest
	if got := statusOf(t, redeem(signed, body)); got != 400 {
		t.Errorf("invalid request: %d", got)
	}
	f.err = pairing.ErrNonceRefused
	if got := statusOf(t, redeem(signed, body)); got != 403 {
		t.Errorf("refused nonce: %d", got)
	}
	f.err = kernel.ErrStore
	if err := redeem(signed, body); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("store fault: %v", err)
	}

	cancel := func(ctx context.Context, use string) error {
		_, err := s.CancelPairing(ctx, peergen.CancelPairingRequestObject{
			Params: peergen.CancelPairingParams{UseCase: peergen.UseCase(use)},
		})
		return err
	}
	if got := statusOf(t, cancel(t.Context(), "federated-repo/*")); got != 401 {
		t.Errorf("cancel without caller: %d", got)
	}
	if got := statusOf(t, cancel(signed, "distribution")); got != 400 {
		t.Errorf("cancel unserved use case: %d", got)
	}
	if err := cancel(signed, "federated-repo/r"); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("cancel store fault: %v", err)
	}
	f.err = nil
	if err := cancel(signed, "federated-repo/r"); err != nil {
		t.Errorf("cancel: %v", err)
	}
	if len(f.dropped) != 2 || f.dropped[1] != "jfrt@b federated-repo/r" {
		t.Errorf("dropped %v", f.dropped)
	}
}
