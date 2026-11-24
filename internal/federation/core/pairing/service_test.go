package pairing_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// store is one instance's metadata: the federation and platform stores
// over one shared in-memory database.
type store struct {
	*fedinmem.Store
	platform *platforminmem.Store
}

func (s store) Outbox() outbox.OutboxStore       { return s.platform.Outbox() }
func (s store) Instance() instance.InstanceStore { return s.platform.Instance() }
func (s store) peers(t *testing.T) []model.Peer {
	return must(s.Peers().List(t.Context(), port.PeerFilter{}))
}

func (s store) identity() instance.InstanceIdentity {
	return must(s.Instance().Get(context.Background()))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// node is one instance: its store, its pairing service and the base URL it
// is reached at.
type node struct {
	store store
	svc   *pairing.Service
	base  string
	clock *time.Time
}

// network routes Remote calls between nodes by base URL, as the signed peer
// client would. down lists the base URLs that do not answer, and answer, if
// set, rewrites an issuer's answer before the redeemer sees it.
type network struct {
	mu     sync.Mutex
	nodes  map[string]*node
	down   map[string]bool
	answer func(pairing.Answer) pairing.Answer
	err    error
	calls  []string
}

type remote struct {
	net  *network
	from *node
}

func (r remote) Pair(ctx context.Context, base string, req pairing.Request) (pairing.Answer, error) {
	r.net.mu.Lock()
	r.net.calls = append(r.net.calls, "pair "+base)
	to, down, rewrite, fault := r.net.nodes[base], r.net.down[base], r.net.answer, r.net.err
	r.net.mu.Unlock()
	if fault != nil {
		return pairing.Answer{}, fault
	}
	if to == nil || down {
		return pairing.Answer{}, pairing.ErrUnreachable
	}
	ans, err := to.svc.Accept(ctx, req, to.base)
	if errors.Is(err, pairing.ErrNonceRefused) {
		return pairing.Answer{}, &pairing.StatusError{Status: 403}
	}
	if errors.Is(err, pairing.ErrInvalidRequest) {
		return pairing.Answer{}, &pairing.StatusError{Status: 400}
	}
	if err != nil {
		return pairing.Answer{}, err
	}
	if rewrite != nil {
		ans = rewrite(ans)
	}
	return ans, nil
}

func (r remote) Unpair(ctx context.Context, p model.Peer, use model.UseCase) error {
	r.net.mu.Lock()
	r.net.calls = append(r.net.calls, "unpair "+p.BaseURL)
	to, down := r.net.nodes[p.BaseURL], r.net.down[p.BaseURL]
	r.net.mu.Unlock()
	if to == nil || down {
		return pairing.ErrUnreachable
	}
	return to.svc.Drop(ctx, r.from.store.identity().InstanceID, use)
}

func newNetwork() *network {
	return &network{nodes: map[string]*node{}, down: map[string]bool{}}
}

func (n *network) add(t *testing.T, base string) *node {
	t.Helper()
	shared := memtx.New()
	st := store{Store: fedinmem.New(shared), platform: platforminmem.New(shared)}
	if err := instance.EnsureInstance(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	clock := t0
	nd := &node{store: st, base: base, clock: &clock}
	nd.svc = &pairing.Service{
		Store:     st,
		Remote:    remote{net: n, from: nd},
		Authorize: func(context.Context) error { return nil },
		Now:       func() time.Time { return *nd.clock },
	}
	n.nodes[base] = nd
	return nd
}

func pair(t *testing.T, issuer, redeemer *node, use model.UseCase) {
	t.Helper()
	tok, err := issuer.svc.Issue(t.Context(), use, issuer.base+"/artifactory/")
	if err != nil {
		t.Fatal("issue", err)
	}
	if err := redeemer.svc.Redeem(t.Context(), use, tok, redeemer.base); err != nil {
		t.Fatal("redeem", err)
	}
}

func refusal(t *testing.T, err error, want string) {
	t.Helper()
	var r *pairing.Refusal
	if !errors.As(err, &r) || r.Reason != want {
		t.Fatalf("got %v, want the refusal %q", err, want)
	}
}

func TestUnitPairBothUseCases(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	pair(t, a, b, model.UseFederatedRepoRead)

	for _, c := range []struct {
		self, other *node
	}{{a, b}, {b, a}} {
		peers := c.self.store.peers(t)
		if len(peers) != 1 {
			t.Fatalf("%s holds %d peers, want 1", c.self.base, len(peers))
		}
		got, want := peers[0], c.other.store.identity()
		if got.InstanceID != want.InstanceID || got.BaseURL != c.other.base ||
			!got.PublicKey.Equal(want.PublicKey) || got.ProtocolVersion != instance.ProtocolVersion ||
			got.Status != model.PeerOnline || !got.LastSeen.Equal(t0) ||
			fmt.Sprint(got.UseCases) != "[federated-repo/* federated-repo/r]" {
			t.Errorf("%s records %+v", c.self.base, got)
		}
		for _, use := range []model.UseCase{model.UseFederatedRepo, model.UseFederatedRepoRead} {
			urls := must(c.self.svc.Pairings(t.Context(), use))
			if fmt.Sprint(urls) != "["+c.other.base+"]" {
				t.Errorf("%s pairings %s = %v", c.self.base, use, urls)
			}
		}
		if urls := must(c.self.svc.Pairings(t.Context(), model.UseDistribution)); len(urls) != 0 {
			t.Errorf("distribution pairings = %v", urls)
		}
	}
}

func TestUnitIssuedTokenCarriesTheIssuer(t *testing.T) {
	net := newNetwork()
	a := net.add(t, "https://a.example.com")
	raw := must(a.svc.Issue(t.Context(), model.UseFederatedRepoRead, "https://a.example.com/artifactory"))
	tok := must(pairing.Parse(raw))
	id := a.store.identity()
	if tok.BaseURL != a.base || tok.InstanceID != id.InstanceID || tok.UseCase != model.UseFederatedRepoRead ||
		tok.Fingerprint != pairing.Fingerprint(id.PublicKey) || tok.IssuedAt != t0.Unix() ||
		tok.ExpiresAt != t0.Add(300*time.Second).Unix() {
		t.Errorf("token = %+v", tok.Token)
	}
	if err := tok.Verify(id.PublicKey); err != nil {
		t.Error(err)
	}
	// Only the nonce's hash is stored, never the token or the nonce.
	nonce := must(tok.NonceBytes())
	if _, err := a.store.Peers().RedeemNonce(t.Context(), nonce, "x", t0); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("the nonce itself is stored: %v", err)
	}
	if _, err := a.store.Peers().RedeemNonce(t.Context(), pairing.NonceHash(nonce), "x", t0); err != nil {
		t.Errorf("the nonce hash is not stored: %v", err)
	}
}

func TestUnitRedeemRefusals(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		*b.clock = t0.Add(300 * time.Second)
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base),
			"Attempt to pair instances with an expired token")
		if len(net.calls) != 0 || len(a.store.peers(t)) != 0 || len(b.store.peers(t)) != 0 {
			t.Errorf("calls %v", net.calls)
		}
		// One second before, the same token pairs.
		*b.clock = t0.Add(299 * time.Second)
		*a.clock = *b.clock
		if err := b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base); err != nil {
			t.Fatal(err)
		}
		// The issuer refuses an expired nonce on its own too.
		tok2 := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		*a.clock = a.clock.Add(time.Hour)
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok2, b.base),
			"Error exchanging token: unacceptable http status code from target server 403")
	})
	t.Run("reused", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		c := net.add(t, "https://c.example.com")
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		if err := b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base); err != nil {
			t.Fatal(err)
		}
		calls := len(net.calls)
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base),
			"The pairing token was already used")
		if len(net.calls) != calls {
			t.Error("a reused token reached the issuer")
		}
		refusal(t, c.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, c.base),
			"Error exchanging token: unacceptable http status code from target server 403")
		if len(c.store.peers(t)) != 0 || len(a.store.peers(t)) != 1 {
			t.Error("a refused redeemer was recorded")
		}
		// C's claim was released with its failure, so C is refused by the
		// issuer again rather than by its own claim.
		refusal(t, c.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, c.base),
			"Error exchanging token: unacceptable http status code from target server 403")
	})
	t.Run("concurrent", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		errs := make(chan error, 2)
		for range 2 {
			go func() { errs <- b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base) }()
		}
		first, second := <-errs, <-errs
		if (first == nil) == (second == nil) {
			t.Fatalf("redeems = %v, %v; want exactly one to pair", first, second)
		}
		loser := first
		if loser == nil {
			loser = second
		}
		refusal(t, loser, "The pairing token was already used")
		if len(a.store.peers(t)) != 1 || len(b.store.peers(t)) != 1 {
			t.Error("two pairings recorded")
		}
	})
	t.Run("tampered", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		raw := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		// Push the expiry out, keeping the issuer's signature.
		parts := strings.Split(raw, ".")
		var claims map[string]any
		if err := json.Unmarshal(must(base64.RawURLEncoding.DecodeString(parts[1])), &claims); err != nil {
			t.Fatal(err)
		}
		claims["exp"] = t0.Add(24 * time.Hour).Unix()
		parts[1] = base64.RawURLEncoding.EncodeToString(must(json.Marshal(claims)))
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, strings.Join(parts, "."), b.base),
			"The pairing token is malformed")
		if len(a.store.peers(t)) != 0 || len(b.store.peers(t)) != 0 {
			t.Error("a tampered token left a peer record")
		}
		// Not a token at all.
		for _, bad := range []string{"", "a.b", "x.y.z", raw + "x"} {
			refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, bad, b.base),
				"The pairing token is malformed")
		}
	})
	t.Run("wrong fingerprint", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		raw := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		// The genuine nonce, re-signed by a key that is not A's.
		tok := must(pairing.Parse(raw)).Token
		pub, priv, _ := ed25519.GenerateKey(nil)
		tok.Fingerprint = pairing.Fingerprint(pub)
		forged := must(tok.Sign(priv))
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, forged, b.base),
			"The issuing instance does not hold the key the pairing token names")
		if len(a.store.peers(t)) != 0 || len(b.store.peers(t)) != 0 {
			t.Error("a wrong-fingerprint token left a peer record")
		}
	})
	t.Run("other use case", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepoRead, a.base))
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base),
			"The pairing token was issued for use case federated-repo/r")
	})
	t.Run("itself", func(t *testing.T) {
		net := newNetwork()
		a := net.add(t, "https://a.example.com")
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		refusal(t, a.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, a.base),
			"Attempt to pair instance with itself")
	})
	t.Run("unreachable", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		net.down[a.base] = true
		refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base),
			"Error exchanging token due to network failure")
		// The claim went with the failure: once the issuer answers, the
		// same token pairs.
		net.down[a.base] = false
		if err := b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnitRedeemChecksTheAnswer(t *testing.T) {
	_, otherKey, _ := ed25519.GenerateKey(nil)
	for name, c := range map[string]struct {
		rewrite func(pairing.Answer) pairing.Answer
		err     error
		want    string
	}{
		"other key": {rewrite: func(a pairing.Answer) pairing.Answer {
			a.PublicKey = otherKey.Public().(ed25519.PublicKey) //nolint:forcetypeassert // ed25519
			return a
		}, want: "The issuing instance does not hold the key the pairing token names"},
		"other instance": {rewrite: func(a pairing.Answer) pairing.Answer {
			a.InstanceID = "jfrt@00000000-0000-4000-8000-000000000000"
			return a
		}, want: "The issuing instance does not hold the key the pairing token names"},
		"other use case": {rewrite: func(a pairing.Answer) pairing.Answer {
			a.UseCase = model.UseFederatedRepoRead
			return a
		}, want: "The issuing instance does not hold the key the pairing token names"},
		"other major": {rewrite: func(a pairing.Answer) pairing.Answer {
			a.ProtocolVersion = "2.0"
			return a
		}, want: "The issuing instance speaks peer protocol 2.0, not 1.x"},
		"no version": {rewrite: func(a pairing.Answer) pairing.Answer {
			a.ProtocolVersion = "one"
			return a
		}, want: "The issuing instance speaks peer protocol one, not 1.x"},
		"unsigned":     {err: pairing.ErrUnverified, want: "The issuing instance's answer is not signed with its key"},
		"other status": {err: &pairing.StatusError{Status: 502}, want: "Error exchanging token: unacceptable http status code from target server 502"},
		"wrapped down": {err: fmt.Errorf("dial: %w", pairing.ErrUnreachable), want: "Error exchanging token due to network failure"},
	} {
		t.Run(name, func(t *testing.T) {
			net := newNetwork()
			a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
			net.answer, net.err = c.rewrite, c.err
			tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
			refusal(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base), c.want)
			if len(a.store.peers(t)) != 0 || len(b.store.peers(t)) != 0 {
				t.Error("a refused answer left a peer record")
			}
		})
	}
	t.Run("internal", func(t *testing.T) {
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		boom := errors.New("boom")
		net.err = boom
		tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
		err := b.svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base)
		var r *pairing.Refusal
		if !errors.Is(err, boom) || errors.As(err, &r) {
			t.Errorf("got %v, want the transport's own error", err)
		}
	})
}

// TestUnitRepairKeepsDelivery pairs a second use case with a peer whose
// record an administrator set to pull: the new grant keeps it, as it keeps
// the record's other administrator fields.
func TestUnitRepairKeepsDelivery(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	p := a.store.peers(t)[0]
	p.Delivery = model.DeliveryPull
	if err := a.store.Peers().Put(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	pair(t, a, b, model.UseFederatedRepoRead)
	if got := a.store.peers(t)[0]; got.Delivery != model.DeliveryPull || len(got.UseCases) != 2 {
		t.Fatalf("record after the second pairing %+v", got)
	}
}

func TestUnitAccept(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	raw := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
	tok := must(pairing.Parse(raw))
	bid := b.store.identity()
	req := pairing.Request{
		Nonce: tok.Nonce, InstanceID: bid.InstanceID, BaseURL: b.base + "/",
		PublicKey: bid.PublicKey, ProtocolVersion: "1.3",
	}
	for name, bad := range map[string]func(*pairing.Request){
		"nonce":    func(r *pairing.Request) { r.Nonce = "!" },
		"short":    func(r *pairing.Request) { r.Nonce = "AAAA" },
		"instance": func(r *pairing.Request) { r.InstanceID = "b" },
		"key":      func(r *pairing.Request) { r.PublicKey = r.PublicKey[:5] },
		"base":     func(r *pairing.Request) { r.BaseURL = "ftp://b" },
		"major":    func(r *pairing.Request) { r.ProtocolVersion = "2.0" },
		"version":  func(r *pairing.Request) { r.ProtocolVersion = "1" },
	} {
		r := req
		bad(&r)
		if _, err := a.svc.Accept(t.Context(), r, a.base); !errors.Is(err, pairing.ErrInvalidRequest) ||
			!errors.Is(err, kernel.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	self := req
	self.InstanceID = a.store.identity().InstanceID
	if _, err := a.svc.Accept(t.Context(), self, a.base); !errors.Is(err, pairing.ErrNonceRefused) {
		t.Errorf("self: %v", err)
	}
	unknown := req
	unknown.Nonce = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, err := a.svc.Accept(t.Context(), unknown, a.base); !errors.Is(err, pairing.ErrNonceRefused) ||
		!errors.Is(err, kernel.ErrForbidden) {
		t.Errorf("unknown nonce: %v", err)
	}

	ans := must(a.svc.Accept(t.Context(), req, a.base+"/artifactory"))
	aid := a.store.identity()
	if ans.InstanceID != aid.InstanceID || ans.BaseURL != a.base || !ans.PublicKey.Equal(aid.PublicKey) ||
		ans.ProtocolVersion != "1.0" || ans.UseCase != model.UseFederatedRepo {
		t.Errorf("answer = %+v", ans)
	}
	// A repeat by the same instance with the same key answers again; the
	// peer record is as the first redeem left it.
	*a.clock = t0.Add(time.Minute)
	again := must(a.svc.Accept(t.Context(), req, a.base))
	if again.InstanceID != ans.InstanceID || again.UseCase != ans.UseCase {
		t.Errorf("repeat = %+v", again)
	}
	peers := a.store.peers(t)
	if len(peers) != 1 || peers[0].BaseURL != b.base || peers[0].InstanceID != bid.InstanceID {
		t.Errorf("peers = %+v", peers)
	}
	// Another key, even from the same instance id, is refused.
	pub, _, _ := ed25519.GenerateKey(nil)
	other := req
	other.PublicKey = pub
	if _, err := a.svc.Accept(t.Context(), other, a.base); !errors.Is(err, pairing.ErrNonceRefused) {
		t.Errorf("other key: %v", err)
	}
	// Once expired, even the redeemer that holds it is refused.
	*a.clock = t0.Add(300 * time.Second)
	if _, err := a.svc.Accept(t.Context(), req, a.base); !errors.Is(err, pairing.ErrNonceRefused) {
		t.Errorf("expired repeat: %v", err)
	}
}

func TestUnitUnpair(t *testing.T) {
	setup := func(t *testing.T) (*network, *node, *node) {
		t.Helper()
		net := newNetwork()
		a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
		pair(t, a, b, model.UseFederatedRepo)
		pair(t, a, b, model.UseFederatedRepoRead)
		for _, n := range []*node{a, b} {
			other := a
			if n == a {
				other = b
			}
			if err := n.store.Outbox().PutCursor(t.Context(), outbox.PeerCursor{
				PeerInstanceID: other.store.identity().InstanceID, Stream: "repo:libs", AckedSeq: 3,
			}); err != nil {
				t.Fatal(err)
			}
			if err := n.store.Resyncs().Flag(t.Context(), model.StreamResync{
				Origin: n.store.identity().InstanceID, Receiver: other.store.identity().InstanceID,
				Stream: "repo:libs", Position: 3, Flagged: t0,
			}); err != nil {
				t.Fatal(err)
			}
		}
		return net, a, b
	}
	cursors := func(n *node) int { return len(must(n.store.Outbox().Cursors(context.Background(), ""))) }
	flags := func(n *node) int { return len(must(n.store.Resyncs().Resyncs(context.Background(), ""))) }

	t.Run("both sides", func(t *testing.T) {
		_, a, b := setup(t)
		if err := b.svc.Unpair(t.Context(), model.UseFederatedRepoRead, a.base+"/", false); err != nil {
			t.Fatal(err)
		}
		for _, n := range []*node{a, b} {
			peers := n.store.peers(t)
			if len(peers) != 1 || fmt.Sprint(peers[0].UseCases) != "[federated-repo/*]" || cursors(n) != 1 || flags(n) != 1 {
				t.Errorf("%s keeps %+v", n.base, peers)
			}
		}
		// The last use case takes the peer and its cursors with it, on both
		// sides, and from either side.
		if err := a.svc.Unpair(t.Context(), model.UseFederatedRepo, b.base, false); err != nil {
			t.Fatal(err)
		}
		for _, n := range []*node{a, b} {
			// A peer's full-sync flags go with its cursors.
			if len(n.store.peers(t)) != 0 || cursors(n) != 0 || flags(n) != 0 {
				t.Errorf("%s keeps a peer, cursor or full-sync flag", n.base)
			}
		}
		refusal(t, a.svc.Unpair(t.Context(), model.UseFederatedRepo, b.base, false),
			"Pairing does not exist for instance")
	})
	t.Run("unreachable", func(t *testing.T) {
		net, a, b := setup(t)
		net.down[a.base] = true
		refusal(t, b.svc.Unpair(t.Context(), model.UseFederatedRepo, a.base, false),
			"Failed calling revoke token endpoint")
		if len(must(b.svc.Pairings(t.Context(), model.UseFederatedRepo))) != 1 {
			t.Error("a refused unpair dropped the trust")
		}
		if err := b.svc.Unpair(t.Context(), model.UseFederatedRepo, a.base, true); err != nil {
			t.Fatal(err)
		}
		if len(must(b.svc.Pairings(t.Context(), model.UseFederatedRepo))) != 0 ||
			len(must(a.svc.Pairings(t.Context(), model.UseFederatedRepo))) != 1 {
			t.Error("force dropped the wrong side")
		}
	})
	t.Run("refusals", func(t *testing.T) {
		_, a, b := setup(t)
		refusal(t, b.svc.Unpair(t.Context(), model.UseFederatedRepo, "", false), "Base url is not provided")
		refusal(t, b.svc.Unpair(t.Context(), model.UseFederatedRepo, "https://c.example.com", true),
			"Pairing does not exist for instance")
		refusal(t, b.svc.Unpair(t.Context(), model.UseDistribution, a.base, true),
			"Pairing does not exist for instance")
	})
	t.Run("rejected signature", func(t *testing.T) {
		_, a, b := setup(t)
		unverified := &pairing.Service{
			Store: b.store, Authorize: b.svc.Authorize, Now: b.svc.Now,
			Remote: failingRemote{pairing.ErrUnverified},
		}
		refusal(t, unverified.Unpair(t.Context(), model.UseFederatedRepo, a.base, false),
			"Failed calling revoke token endpoint")
		p := b.store.peers(t)[0]
		if p.Status != model.PeerUnauthorized || unverified.Status(p, 0) != model.PeerUnauthorized {
			t.Errorf("status = %s", p.Status)
		}
	})
	t.Run("drop unknown", func(t *testing.T) {
		_, a, _ := setup(t)
		if err := a.svc.Drop(t.Context(), "jfrt@00000000-0000-4000-8000-000000000009", model.UseFederatedRepo); err != nil {
			t.Error(err)
		}
	})
}

type failingRemote struct{ err error }

func (f failingRemote) Pair(context.Context, string, pairing.Request) (pairing.Answer, error) {
	return pairing.Answer{}, f.err
}

func (f failingRemote) Unpair(context.Context, model.Peer, model.UseCase) error { return f.err }

func TestUnitStatusAndPrune(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	id := a.store.identity().InstanceID
	p := b.store.peers(t)[0]
	if b.svc.Status(p, 0) != model.PeerOnline || b.svc.Status(p, 1) != model.PeerPartial {
		t.Error("a fresh peer is online")
	}
	*b.clock = t0.Add(11 * time.Minute)
	if b.svc.Status(p, 0) != model.PeerOffline {
		t.Error("a peer unseen beyond the stale window is offline")
	}
	if err := b.svc.Observe(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	if p := b.store.peers(t)[0]; p.Status != model.PeerUnauthorized || !p.LastSeen.Equal(t0) {
		t.Errorf("rejected: %+v", p)
	}
	if err := b.svc.Observe(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if p := b.store.peers(t)[0]; p.Status != model.PeerOnline || !p.LastSeen.Equal(*b.clock) {
		t.Errorf("seen: %+v", p)
	}
	if err := b.svc.Observe(t.Context(), "jfrt@00000000-0000-4000-8000-000000000009", false); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("unknown peer: %v", err)
	}

	// A token never redeemed is never cancelled; pruning clears it once
	// it has expired, and B's claim on the redeemed one alike.
	_ = must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
	if n := must(a.svc.Prune(t.Context())); n != 0 {
		t.Errorf("pruned %d live nonces", n)
	}
	*a.clock = t0.Add(301 * time.Second)
	if n := must(a.svc.Prune(t.Context())); n != 2 {
		t.Errorf("pruned %d, want the redeemed and the unredeemed nonce", n)
	}
	*b.clock = *a.clock
	if n := must(b.svc.Prune(t.Context())); n != 1 {
		t.Errorf("pruned %d of B's claims, want 1", n)
	}
}

func TestUnitAuthorityAndStoreFaults(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	tok := must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
	for name, authorize := range map[string]func(context.Context) error{
		"refused": func(context.Context) error { return kernel.ErrForbidden },
		"unset":   nil,
	} {
		svc := *b.svc
		svc.Authorize = authorize
		calls := []error{
			func() error { _, err := svc.Issue(t.Context(), model.UseFederatedRepo, b.base); return err }(),
			svc.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base),
			func() error { _, err := svc.Pairings(t.Context(), model.UseFederatedRepo); return err }(),
			svc.Unpair(t.Context(), model.UseFederatedRepo, a.base, true),
		}
		for i, err := range calls {
			if err == nil {
				t.Errorf("%s: call %d was allowed", name, i)
			}
		}
	}
	if len(net.calls) != 0 {
		t.Errorf("refused callers reached a peer: %v", net.calls)
	}
	noRemote := *b.svc
	noRemote.Remote = nil
	if err := noRemote.Redeem(t.Context(), model.UseFederatedRepo, tok, b.base); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("no transport: %v", err)
	}
	pair(t, a, b, model.UseFederatedRepo)
	if err := noRemote.Unpair(t.Context(), model.UseFederatedRepo, a.base, false); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("no transport: %v", err)
	}

	// A store that is gone fails every call with its error, not a refusal.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for i, err := range []error{
		func() error { _, err := b.svc.Issue(ctx, model.UseFederatedRepo, b.base); return err }(),
		b.svc.Redeem(ctx, model.UseFederatedRepo, tok, b.base),
		func() error { _, err := b.svc.Pairings(ctx, model.UseFederatedRepo); return err }(),
		b.svc.Unpair(ctx, model.UseFederatedRepo, a.base, true),
		func() error { _, err := a.svc.Accept(ctx, pairing.Request{}, a.base); return err }(),
		func() error { _, err := a.svc.Prune(ctx); return err }(),
		b.svc.Drop(ctx, a.store.identity().InstanceID, model.UseFederatedRepo),
	} {
		var r *pairing.Refusal
		if err == nil || errors.As(err, &r) {
			t.Errorf("call %d on a cancelled context: %v", i, err)
		}
	}
	short := *a.svc
	short.Rand = strings.NewReader("too short")
	if _, err := short.Issue(t.Context(), model.UseFederatedRepo, a.base); err == nil {
		t.Error("a short random source issued a token")
	}
}

func TestUnitVersions(t *testing.T) {
	if pairing.Version(1) != "1.0" {
		t.Error(pairing.Version(1))
	}
	for v, want := range map[string]int{"1.0": 1, "12.7": 12} {
		if got, ok := pairing.Major(v); !ok || got != want {
			t.Errorf("Major(%q) = %d %v", v, got, ok)
		}
	}
	for _, v := range []string{"", "1", "a.0", "1.b", "-1.0"} {
		if _, ok := pairing.Major(v); ok {
			t.Errorf("Major(%q) parsed", v)
		}
	}
	for in, want := range map[string]string{
		"https://a.example.com":                "https://a.example.com",
		"https://a.example.com/":               "https://a.example.com",
		"https://a.example.com/artifactory":    "https://a.example.com",
		"https://a.example.com/artifactory/":   "https://a.example.com",
		"https://a.example.com/ui/artifactory": "https://a.example.com/ui",
	} {
		if got := pairing.PeerBaseURL(in); got != want {
			t.Errorf("PeerBaseURL(%q) = %q", in, got)
		}
	}
	if (&pairing.StatusError{Status: 418}).Error() != "peer answered 418" {
		t.Error("StatusError")
	}
	if (&pairing.Refusal{Reason: "r"}).Error() != "r" {
		t.Error("Refusal")
	}
}

func TestUnitTokenParse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	tok := pairing.Token{
		BaseURL: "https://a", InstanceID: "jfrt@x", Fingerprint: pairing.Fingerprint(pub),
		UseCase: model.UseFederatedRepo, Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		IssuedAt: 1, ExpiresAt: 2,
	}
	raw := must(tok.Sign(priv))
	parsed := must(pairing.Parse(raw))
	if parsed.Token != tok || parsed.Verify(pub) != nil {
		t.Fatalf("round trip: %+v", parsed.Token)
	}
	if !tok.Expires().Equal(time.Unix(2, 0)) {
		t.Error("Expires")
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if !errors.Is(parsed.Verify(other), pairing.ErrKeyMismatch) || !errors.Is(parsed.Verify(pub[:4]), pairing.ErrKeyMismatch) {
		t.Error("a key the token does not name verified")
	}
	parts := strings.Split(raw, ".")
	sig := must(base64.RawURLEncoding.DecodeString(parts[2]))
	sig[0] ^= 1
	flipped := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
	if err := must(pairing.Parse(flipped)).Verify(pub); !errors.Is(err, pairing.ErrTokenSignature) {
		t.Errorf("a flipped signature: %v", err)
	}
	enc := func(v any) string { return base64.RawURLEncoding.EncodeToString(must(json.Marshal(v))) }
	claims := func(edit func(map[string]any)) string {
		m := map[string]any{}
		_ = json.Unmarshal(must(json.Marshal(tok)), &m)
		edit(m)
		return enc(m)
	}
	for name, bad := range map[string]string{
		"parts":      "a.b",
		"header b64": "!." + parts[1] + "." + parts[2],
		"alg":        enc(map[string]string{"alg": "none"}) + "." + parts[1] + "." + parts[2],
		"header":     enc("x") + "." + parts[1] + "." + parts[2],
		"payload":    parts[0] + ".!." + parts[2],
		"json":       parts[0] + "." + enc("x") + "." + parts[2],
		"sig b64":    parts[0] + "." + parts[1] + ".!",
		"sig size":   parts[0] + "." + parts[1] + ".AAAA",
		"base":       parts[0] + "." + claims(func(m map[string]any) { delete(m, "base_url") }) + "." + parts[2],
		"exp":        parts[0] + "." + claims(func(m map[string]any) { delete(m, "exp") }) + "." + parts[2],
		"key":        parts[0] + "." + claims(func(m map[string]any) { m["key_sha256"] = "ab" }) + "." + parts[2],
		"nonce":      parts[0] + "." + claims(func(m map[string]any) { m["nonce"] = "AAAA" }) + "." + parts[2],
	} {
		if _, err := pairing.Parse(bad); !errors.Is(err, pairing.ErrMalformedToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A claim a later issuer adds is ignored.
	extra := parts[0] + "." + claims(func(m map[string]any) { m["added"] = true }) + "." + parts[2]
	if _, err := pairing.Parse(extra); err != nil {
		t.Errorf("an added claim: %v", err)
	}
}

// faultStore fails the named store calls with errStore.
type faultStore struct {
	store
	fail map[string]bool
}

var errStore = fmt.Errorf("injected: %w", kernel.ErrStore)

func (f faultStore) Peers() port.PeerStore { return faultPeers{f.store.Peers(), f.fail} }

func (f faultStore) Outbox() outbox.OutboxStore { return faultOutbox{f.store.Outbox(), f.fail} }

func (f faultStore) Resyncs() port.StreamResyncStore { return faultResyncs{f.store.Resyncs(), f.fail} }

type faultResyncs struct {
	port.StreamResyncStore
	fail map[string]bool
}

func (f faultResyncs) DeletePeer(ctx context.Context, id string) error {
	if f.fail["DeletePeer"] {
		return errStore
	}
	return f.StreamResyncStore.DeletePeer(ctx, id)
}

type faultPeers struct {
	port.PeerStore
	fail map[string]bool
}

func (f faultPeers) Get(ctx context.Context, id string) (model.Peer, error) {
	if f.fail["Get"] {
		return model.Peer{}, errStore
	}
	return f.PeerStore.Get(ctx, id)
}

func (f faultPeers) Put(ctx context.Context, p model.Peer) error {
	if f.fail["Put"] {
		return errStore
	}
	return f.PeerStore.Put(ctx, p)
}

func (f faultPeers) Delete(ctx context.Context, id string) error {
	if f.fail["Delete"] {
		return errStore
	}
	return f.PeerStore.Delete(ctx, id)
}

func (f faultPeers) AddNonce(ctx context.Context, n model.PairingNonce) error {
	if f.fail["AddNonce"] {
		return errStore
	}
	return f.PeerStore.AddNonce(ctx, n)
}

type faultOutbox struct {
	outbox.OutboxStore
	fail map[string]bool
}

func (f faultOutbox) DeleteCursors(ctx context.Context, peer string) error {
	if f.fail["DeleteCursors"] {
		return errStore
	}
	return f.OutboxStore.DeleteCursors(ctx, peer)
}

func TestUnitStoreFaults(t *testing.T) {
	net := newNetwork()
	a, b := net.add(t, "https://a.example.com"), net.add(t, "https://b.example.com")
	pair(t, a, b, model.UseFederatedRepo)
	pair(t, a, b, model.UseFederatedRepoRead)
	aid := a.store.identity().InstanceID
	faulty := func(n *node, fail ...string) *pairing.Service {
		set := map[string]bool{}
		for _, f := range fail {
			set[f] = true
		}
		svc := *n.svc
		svc.Store = faultStore{n.store, set}
		return &svc
	}
	for name, err := range map[string]error{
		"issue AddNonce": func() error {
			_, err := faulty(a, "AddNonce").Issue(t.Context(), model.UseFederatedRepo, a.base)
			return err
		}(),
		"redeem AddNonce": faulty(b, "AddNonce").Redeem(t.Context(), model.UseFederatedRepo, must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base)), b.base),
		"redeem Get":      faulty(b, "Get").Redeem(t.Context(), model.UseFederatedRepo, must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base)), b.base),
		"redeem Put":      faulty(b, "Put").Redeem(t.Context(), model.UseFederatedRepo, must(a.svc.Issue(t.Context(), model.UseFederatedRepo, a.base)), b.base),
		"drop Get":        faulty(b, "Get").Drop(t.Context(), aid, model.UseFederatedRepo),
		"drop Put":        faulty(b, "Put").Drop(t.Context(), aid, model.UseFederatedRepo),
		"observe Put":     faulty(b, "Put").Observe(t.Context(), aid, false),
	} {
		if !errors.Is(err, kernel.ErrStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := b.svc.Drop(t.Context(), aid, model.UseFederatedRepoRead); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"drop Delete":        faulty(b, "Delete").Drop(t.Context(), aid, model.UseFederatedRepo),
		"drop DeleteCursors": faulty(b, "DeleteCursors").Drop(t.Context(), aid, model.UseFederatedRepo),
		"drop DeletePeer":    faulty(b, "DeletePeer").Drop(t.Context(), aid, model.UseFederatedRepo),
	} {
		if !errors.Is(err, kernel.ErrStore) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A refused drop changes nothing: the peer keeps its last use case.
	if urls := must(b.svc.Pairings(t.Context(), model.UseFederatedRepo)); len(urls) != 1 {
		t.Errorf("pairings after refused drops = %v", urls)
	}
}

func TestUnitDefaults(t *testing.T) {
	net := newNetwork()
	a := net.add(t, "https://a.example.com")
	svc := pairing.Service{Store: a.store, Authorize: a.svc.Authorize, StaleAfter: time.Hour}
	raw := must(svc.Issue(t.Context(), model.UseFederatedRepo, a.base))
	tok := must(pairing.Parse(raw))
	if d := time.Since(time.Unix(tok.IssuedAt, 0)); d < 0 || d > time.Minute {
		t.Errorf("issued %v ago with the default clock", d)
	}
	p := model.Peer{LastSeen: time.Now().Add(-30 * time.Minute), ProtocolVersion: instance.ProtocolVersion}
	if svc.Status(p, 0) != model.PeerOnline {
		t.Error("StaleAfter is not honoured")
	}
}
