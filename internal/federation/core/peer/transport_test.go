package peer_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const (
	callerID = "jfrt@00000001-0000-4000-8000-000000000001"
	// testNonce is distinctive, so a refusal that repeated it would show.
	testNonce = "Zm9yLXRoZS10ZXN0LW9ubHk"
	target    = "https://a.example.com/artifactweave/peer/v1/streams/repo%3Alibs/events?after=3"
)

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// clock is an injected clock the tests move.
type clock struct{ now atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.now.Store(t0.UnixNano())
	return c
}

func (c *clock) Now() time.Time          { return time.Unix(0, c.now.Load()).UTC() }
func (c *clock) Advance(d time.Duration) { c.now.Add(int64(d)) }

func key(seed byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	priv := ed25519.NewKeyFromSeed(s)
	return priv.Public().(ed25519.PublicKey), priv //nolint:forcetypeassert // ed25519 public keys are this type
}

type world struct {
	store    *fedinmem.Store
	clock    *clock
	verifier *corepeer.Verifier
	signer   corepeer.Signer
	caller   model.Peer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	pub, priv := key(1)
	w := &world{store: fedinmem.New(memtx.New()), clock: newClock()}
	w.caller = model.Peer{
		InstanceID: callerID, BaseURL: "https://b.example.com", PublicKey: pub,
		ProtocolVersion: instance.ProtocolVersion, UseCases: []model.UseCase{model.UseFederatedRepo},
	}
	if err := w.store.Peers().Put(t.Context(), w.caller); err != nil {
		t.Fatal(err)
	}
	w.verifier = &corepeer.Verifier{Keys: w.store.Peers(), Nonces: w.store.RequestNonces(), Now: w.clock.Now}
	w.signer = corepeer.Signer{KeyID: callerID, Key: priv, Now: w.clock.Now}
	return w
}

// signed returns a request the caller signed.
func (w *world) signed(t *testing.T, method string, body []byte) corepeer.Request {
	t.Helper()
	h := http.Header{}
	if err := w.signer.SignRequest(method, target, h, body); err != nil {
		t.Fatal(err)
	}
	return corepeer.Request{Method: method, TargetURI: target, Header: h, Body: body}
}

// TestUnitSignedRequestVerifies is the round trip: a request signed by a
// paired peer verifies once, names the caller, and carries the protocol
// version and the headers the wire contract lists.
func TestUnitSignedRequestVerifies(t *testing.T) {
	w := newWorld(t)
	for _, body := range [][]byte{nil, []byte(`{"origin":"x"}`)} {
		r := w.signed(t, http.MethodPost, body)
		if got := r.Header.Get(corepeer.HeaderProtocol); got != "1.0" {
			t.Errorf("protocol header %q, want 1.0", got)
		}
		if (r.Header.Get(corepeer.HeaderContentDigest) != "") != (body != nil) {
			t.Errorf("Content-Digest %q with body %q", r.Header.Get(corepeer.HeaderContentDigest), body)
		}
		in := r.Header.Get(corepeer.HeaderSignatureInput)
		for _, want := range []string{`"@method"`, `"@target-uri"`, `"date"`, `keyid="` + callerID + `"`, `alg="ed25519"`, `nonce="`} {
			if !strings.Contains(in, want) {
				t.Errorf("Signature-Input %s lacks %s", in, want)
			}
		}
		got, err := w.verifier.VerifyRequest(t.Context(), r)
		if err != nil {
			t.Fatalf("VerifyRequest: %v", err)
		}
		if got.InstanceID != callerID {
			t.Errorf("caller %s", got.InstanceID)
		}
	}
}

// TestUnitVerifyRequestRefusals refuses each way a request can fail, and
// a refused request takes no place in the replay cache.
func TestUnitVerifyRequestRefusals(t *testing.T) {
	body := []byte(`{"events":[]}`)
	cases := []struct {
		name   string
		mutate func(*world, *corepeer.Request)
		want   error
	}{
		{"unknown key", func(w *world, r *corepeer.Request) {
			_ = w.store.Peers().Delete(context.Background(), callerID)
		}, corepeer.ErrUnknownKey},
		{"bad signature", func(w *world, r *corepeer.Request) {
			_, other := key(9)
			w.signer.Key = other
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{Nonce: testNonce})
		}, corepeer.ErrBadSignature},
		{"altered body", func(_ *world, r *corepeer.Request) { r.Body = []byte(`{"events":[1]}`) }, corepeer.ErrDigest},
		{"altered digest", func(_ *world, r *corepeer.Request) {
			r.Body = []byte(`{"events":[1]}`)
			r.Header.Set(corepeer.HeaderContentDigest, corepeer.ContentDigest(r.Body))
		}, corepeer.ErrBadSignature},
		{"altered path", func(_ *world, r *corepeer.Request) {
			r.TargetURI = strings.Replace(r.TargetURI, "libs", "other", 1)
		}, corepeer.ErrBadSignature},
		{"altered method", func(_ *world, r *corepeer.Request) { r.Method = http.MethodPut }, corepeer.ErrBadSignature},
		{"other host", func(_ *world, r *corepeer.Request) {
			r.TargetURI = strings.Replace(r.TargetURI, "a.example.com", "c.example.com", 1)
		}, corepeer.ErrBadSignature},
		{"no Signature", func(_ *world, r *corepeer.Request) { r.Header.Del(corepeer.HeaderSignature) }, corepeer.ErrUnsigned},
		{"no Signature-Input", func(_ *world, r *corepeer.Request) { r.Header.Del(corepeer.HeaderSignatureInput) }, corepeer.ErrUnsigned},
		{"no Date", func(_ *world, r *corepeer.Request) { r.Header.Del(corepeer.HeaderDate) }, corepeer.ErrUnsigned},
		{"no Content-Digest", func(_ *world, r *corepeer.Request) { r.Header.Del(corepeer.HeaderContentDigest) }, corepeer.ErrBadSignature},
		{"bad Date", func(_ *world, r *corepeer.Request) { r.Header.Set(corepeer.HeaderDate, "yesterday") }, corepeer.ErrSkew},
		{"Date changed", func(w *world, r *corepeer.Request) {
			r.Header.Set(corepeer.HeaderDate, w.clock.Now().Add(time.Minute).Format(http.TimeFormat))
		}, corepeer.ErrBadSignature},
		{"skew over five minutes", func(w *world, _ *corepeer.Request) { w.clock.Advance(5*time.Minute + time.Second) }, corepeer.ErrSkew},
		{"from the future", func(w *world, _ *corepeer.Request) { w.clock.Advance(-5*time.Minute - time.Second) }, corepeer.ErrSkew},
		{"body without digest coverage", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, nil, corepeer.RequestComponents(false), corepeer.Params{Nonce: testNonce})
			r.Body = body
			r.Header.Set(corepeer.HeaderContentDigest, corepeer.ContentDigest(body))
		}, corepeer.ErrCoverage},
		{"no nonce", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{})
		}, corepeer.ErrCoverage},
		{"long nonce", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{Nonce: strings.Repeat("n", 257)})
		}, corepeer.ErrCoverage},
		{"other algorithm", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{Nonce: testNonce, Alg: "rsa-pss-sha512"})
		}, corepeer.ErrCoverage},
		{"no keyid", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{Nonce: testNonce, KeyID: "-"})
		}, corepeer.ErrCoverage},
		{"no created", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{Nonce: testNonce, Created: time.Time{}.Add(1)})
		}, corepeer.ErrCoverage},
		{"expired", func(w *world, r *corepeer.Request) {
			*r = w.signedWith(r.Method, r.Body, corepeer.RequestComponents(true), corepeer.Params{Nonce: testNonce, Expires: w.clock.Now()})
		}, corepeer.ErrSkew},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			r := w.signed(t, http.MethodPost, body)
			c.mutate(w, &r)
			_, err := w.verifier.VerifyRequest(t.Context(), r)
			if !errors.Is(err, c.want) {
				t.Fatalf("VerifyRequest: %v, want %v", err, c.want)
			}
			// A refusal is logged and answered as it is, so it must name
			// neither the signature nor the nonce.
			sig, _ := corepeer.ParseSignature(r.Header)
			for _, secret := range []string{sig.Params.Nonce, r.Header.Get(corepeer.HeaderSignature)} {
				if secret != "" && strings.Contains(err.Error(), strings.TrimSuffix(strings.TrimPrefix(secret, "peer=:"), ":")) {
					t.Errorf("the refusal %q names a signature or nonce", err)
				}
			}
			if n, _ := w.store.RequestNonces().Prune(t.Context(), t0.Add(time.Hour)); n != 0 {
				t.Errorf("a refused request took %d places in the replay cache", n)
			}
		})
	}
}

// signedWith signs a request with custom coverage and parameters; zero
// fields of p take the signer's values, "-" leaves keyid out.
func (w *world) signedWith(method string, body []byte, covered []corepeer.Component, p corepeer.Params) corepeer.Request {
	h := http.Header{}
	h.Set(corepeer.HeaderDate, w.clock.Now().Format(http.TimeFormat))
	if body != nil {
		h.Set(corepeer.HeaderContentDigest, corepeer.ContentDigest(body))
	}
	if p.Created.IsZero() {
		p.Created = w.clock.Now()
	} else if p.Created.Equal(time.Time{}.Add(1)) {
		p.Created = time.Time{}
	}
	switch p.KeyID {
	case "":
		p.KeyID = callerID
	case "-":
		p.KeyID = ""
	}
	in, sig, err := corepeer.Sign(corepeer.Message{Method: method, TargetURI: target, Header: h}, "peer", covered, p, w.signer.Key)
	if err != nil {
		panic(err)
	}
	h.Set(corepeer.HeaderSignatureInput, in)
	h.Set(corepeer.HeaderSignature, sig)
	return corepeer.Request{Method: method, TargetURI: target, Header: h, Body: body}
}

// TestUnitReplayRefused refuses a replayed nonce for the whole skew window,
// accepts it again only once the request could no longer pass the skew
// check anyway, and keeps two callers' equal nonces apart.
func TestUnitReplayRefused(t *testing.T) {
	w := newWorld(t)
	r := w.signed(t, http.MethodGet, nil)
	if _, err := w.verifier.VerifyRequest(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	for _, d := range []time.Duration{0, 4 * time.Minute, time.Minute - time.Second} {
		w.clock.Advance(d)
		if _, err := w.verifier.VerifyRequest(t.Context(), r); !errors.Is(err, corepeer.ErrReplay) {
			t.Fatalf("replay after %v: %v, want ErrReplay", d, err)
		}
	}
	w.clock.Advance(2 * time.Second)
	if _, err := w.verifier.VerifyRequest(t.Context(), r); !errors.Is(err, corepeer.ErrSkew) {
		t.Fatalf("replay past the window: %v, want ErrSkew", err)
	}

	// Pruning runs at most once per window; its entries are gone after.
	w.clock.Advance(6 * time.Minute)
	if _, err := w.verifier.VerifyRequest(t.Context(), w.signed(t, http.MethodGet, nil)); err != nil {
		t.Fatal(err)
	}
	if n, _ := w.store.RequestNonces().Prune(t.Context(), w.clock.Now().Add(time.Hour)); n != 1 {
		t.Errorf("%d entries left after pruning, want only the fresh one", n)
	}
}

// TestUnitReplayRace sends one signed request from many goroutines at once:
// exactly one is accepted.
func TestUnitReplayRace(t *testing.T) {
	w := newWorld(t)
	r := w.signed(t, http.MethodPost, []byte(`{}`))
	const n = 32
	var (
		ok      atomic.Int32
		replays atomic.Int32
		wg      sync.WaitGroup
	)
	for range n {
		wg.Go(func() {
			_, err := w.verifier.VerifyRequest(context.Background(), corepeer.Request{
				Method: r.Method, TargetURI: r.TargetURI, Header: r.Header.Clone(), Body: r.Body,
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, corepeer.ErrReplay):
				replays.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || replays.Load() != n-1 {
		t.Errorf("%d accepted and %d replays, want 1 and %d", ok.Load(), replays.Load(), n-1)
	}
}

// failingNonces is a replay cache whose store has failed.
type failingNonces struct{}

var errStore = errors.New("store down")

func (failingNonces) Remember(context.Context, []byte, time.Time, time.Time) error { return errStore }

func (failingNonces) Prune(context.Context, time.Time) (int64, error) { return 0, errStore }

type failingKeys struct{ err error }

func (f failingKeys) Get(context.Context, string) (model.Peer, error)       { return model.Peer{}, f.err }
func (f failingKeys) SetProtocolVersion(context.Context, string, int) error { return f.err }

// TestUnitVerifierStoreFailures reports a failed store as a failure, never
// as a verdict on the request.
func TestUnitVerifierStoreFailures(t *testing.T) {
	w := newWorld(t)
	v := &corepeer.Verifier{Keys: w.store.Peers(), Nonces: failingNonces{}, Now: w.clock.Now}
	if _, err := v.VerifyRequest(t.Context(), w.signed(t, http.MethodGet, nil)); !errors.Is(err, errStore) {
		t.Errorf("a failed replay cache: %v", err)
	}
	v = &corepeer.Verifier{Keys: failingKeys{errStore}, Nonces: w.store.RequestNonces(), Now: w.clock.Now}
	if _, err := v.VerifyRequest(t.Context(), w.signed(t, http.MethodGet, nil)); !errors.Is(err, errStore) {
		t.Errorf("a failed peer store: %v", err)
	}
	if err := v.ObserveVersion(t.Context(), w.caller, corepeer.Version{Major: 2}); !errors.Is(err, errStore) {
		t.Errorf("ObserveVersion over a failed store: %v", err)
	}
	v = &corepeer.Verifier{Keys: failingKeys{kernel.ErrNotFound}, Nonces: w.store.RequestNonces()}
	if err := v.ObserveVersion(t.Context(), w.caller, corepeer.Version{Major: 2}); err != nil {
		t.Errorf("ObserveVersion of a peer removed meanwhile: %v", err)
	}
}

// TestUnitBootstrapCaller verifies POST /pair with the key its body
// presents, and only when keyid names the presented instance.
func TestUnitBootstrapCaller(t *testing.T) {
	w := newWorld(t)
	pub, priv := key(7)
	stranger := &model.Peer{InstanceID: "jfrt@00000007-0000-4000-8000-000000000007", PublicKey: pub}
	s := corepeer.Signer{KeyID: stranger.InstanceID, Key: priv, Now: w.clock.Now}
	h := http.Header{}
	if err := s.SignRequest(http.MethodPost, target, h, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	r := corepeer.Request{Method: http.MethodPost, TargetURI: target, Header: h, Body: []byte(`{}`), Bootstrap: stranger}
	got, err := w.verifier.VerifyRequest(t.Context(), r)
	if err != nil || got.InstanceID != stranger.InstanceID {
		t.Fatalf("bootstrap = %+v, %v", got, err)
	}
	r.Bootstrap = &model.Peer{InstanceID: callerID, PublicKey: pub}
	if _, err := w.verifier.VerifyRequest(t.Context(), r); !errors.Is(err, corepeer.ErrUnknownKey) {
		t.Errorf("a keyid other than the presented instance: %v", err)
	}
	r.Bootstrap = nil
	if _, err := w.verifier.VerifyRequest(t.Context(), r); !errors.Is(err, corepeer.ErrUnknownKey) {
		t.Errorf("an unpaired key without bootstrap: %v", err)
	}
}

// TestUnitObserveVersion records a mismatched major version on the peer
// record and clears it when the peer speaks this build's again.
func TestUnitObserveVersion(t *testing.T) {
	w := newWorld(t)
	if err := w.verifier.ObserveVersion(t.Context(), w.caller, corepeer.Version{Major: 1, Minor: 7}); err != nil {
		t.Fatal(err)
	}
	if err := w.verifier.ObserveVersion(t.Context(), w.caller, corepeer.Version{Major: 2}); err != nil {
		t.Fatal(err)
	}
	p, _ := w.store.Peers().Get(t.Context(), callerID)
	if p.ProtocolVersion != 2 || p.Compatible() || len(p.UseCases) != 1 {
		t.Errorf("after a mismatch the record is %+v", p)
	}
	if err := w.verifier.ObserveVersion(t.Context(), p, corepeer.CurrentVersion); err != nil {
		t.Fatal(err)
	}
	if p, _ = w.store.Peers().Get(t.Context(), callerID); !p.Compatible() {
		t.Errorf("after a compatible request the record is %+v", p)
	}
}

// TestUnitSignedResponse binds an answer to the request it answers and
// refuses a tampered or misattributed one.
func TestUnitSignedResponse(t *testing.T) {
	w := newWorld(t)
	answerPub, answerPriv := key(2)
	answerer := corepeer.Signer{KeyID: "jfrt@a", Key: answerPriv, Now: w.clock.Now}
	req := w.signed(t, http.MethodGet, nil)
	reqMsg := corepeer.Message{Method: req.Method, TargetURI: req.TargetURI, Header: req.Header}
	body := []byte(`{"ok":true}`)
	answer := func() corepeer.Message {
		h := http.Header{}
		h.Set(corepeer.HeaderContentDigest, corepeer.ContentDigest(body))
		if err := answerer.SignResponse(http.StatusOK, h, reqMsg); err != nil {
			t.Fatal(err)
		}
		return corepeer.Message{Status: http.StatusOK, Header: h, Request: &reqMsg}
	}
	if err := corepeer.VerifyResponse(answer(), body, "jfrt@a", answerPub, w.clock.Now()); err != nil {
		t.Fatalf("VerifyResponse: %v", err)
	}
	other := w.signed(t, http.MethodGet, nil)
	for name, c := range map[string]struct {
		mutate func(*corepeer.Message, *[]byte)
		keyID  string
		want   error
	}{
		"tampered body": {func(_ *corepeer.Message, b *[]byte) { *b = []byte(`{"ok":false}`) }, "jfrt@a", corepeer.ErrDigest},
		"tampered status": {
			func(m *corepeer.Message, _ *[]byte) { m.Status = http.StatusCreated }, "jfrt@a",
			corepeer.ErrBadSignature,
		},
		"answer to another request": {func(m *corepeer.Message, _ *[]byte) {
			m.Request = &corepeer.Message{Method: other.Method, TargetURI: other.TargetURI, Header: other.Header}
		}, "jfrt@a", corepeer.ErrBadSignature},
		"another signer": {func(*corepeer.Message, *[]byte) {}, "jfrt@b", corepeer.ErrUnknownKey},
		"unsigned": {
			func(m *corepeer.Message, _ *[]byte) { m.Header.Del(corepeer.HeaderSignature) }, "jfrt@a",
			corepeer.ErrUnsigned,
		},
		"stale": {func(m *corepeer.Message, _ *[]byte) { w.clock.Advance(6 * time.Minute) }, "jfrt@a", corepeer.ErrSkew},
		"request-side coverage only": {func(m *corepeer.Message, _ *[]byte) {
			in, sig, _ := corepeer.Sign(*m, "peer", []corepeer.Component{{Name: "@status"}},
				corepeer.Params{Created: w.clock.Now(), KeyID: "jfrt@a"}, answerPriv)
			m.Header.Set(corepeer.HeaderSignatureInput, in)
			m.Header.Set(corepeer.HeaderSignature, sig)
		}, "jfrt@a", corepeer.ErrCoverage},
	} {
		t.Run(name, func(t *testing.T) {
			m, b := answer(), slicesClone(body)
			c.mutate(&m, &b)
			if err := corepeer.VerifyResponse(m, b, c.keyID, answerPub, w.clock.Now()); !errors.Is(err, c.want) {
				t.Errorf("VerifyResponse: %v, want %v", err, c.want)
			}
			w.clock.now.Store(t0.UnixNano())
		})
	}
	if err := answerer.SignResponse(http.StatusOK, http.Header{}, reqMsg); !errors.Is(err, corepeer.ErrDigest) {
		t.Errorf("an answer without Content-Digest was signed: %v", err)
	}
}

func slicesClone(b []byte) []byte { return append([]byte(nil), b...) }

// TestUnitSignerFailures refuses to sign without a key or randomness.
func TestUnitSignerFailures(t *testing.T) {
	if err := (corepeer.Signer{}).SignRequest(http.MethodGet, target, http.Header{}, nil); err == nil {
		t.Error("signed without a key")
	}
	_, priv := key(3)
	s := corepeer.Signer{KeyID: "k", Key: priv, Rand: iotest.ErrReader(errStore)}
	if err := s.SignRequest(http.MethodGet, target, http.Header{}, nil); !errors.Is(err, errStore) {
		t.Errorf("signed without a nonce: %v", err)
	}
	s = corepeer.Signer{KeyID: "k", Key: priv}
	if err := s.SignRequest(http.MethodGet, "relative", http.Header{}, nil); err == nil {
		t.Error("signed a relative target")
	}
	h := http.Header{}
	if err := s.SignRequest(http.MethodGet, target, h, nil); err != nil {
		t.Fatal(err)
	}
	if d, err := http.ParseTime(h.Get("Date")); err != nil || time.Since(d) > time.Minute {
		t.Errorf("the default clock dates %q", h.Get("Date"))
	}
}

// TestUnitContentDigest checks RFC 9530's sha-256 example and RFC 9421's
// sha-512 test request digest, and refuses mismatches and malformed fields.
func TestUnitContentDigest(t *testing.T) {
	body := []byte(`{"hello": "world"}`)
	want := "sha-256=:X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE=:"
	if got := corepeer.ContentDigest(body); got != want {
		t.Errorf("ContentDigest = %s, want %s", got, want)
	}
	sum := sha256.Sum256(body)
	if got := corepeer.DigestFromSHA256(sum[:]); got != want {
		t.Errorf("DigestFromSHA256 = %s", got)
	}
	both := want + ", sha-512=:WZDPaVn/7XgHaAy8pmojAkGWoRx2UFChF41A2svX+TaPm+AbwAgBWnrIiYllu7BNNyealdVLvRwEmTHWXvJwew==:, md5=:AA==:"
	if err := corepeer.CheckContentDigest(both, body); err != nil {
		t.Errorf("CheckContentDigest: %v", err)
	}
	for name, value := range map[string]string{
		"mismatch":        corepeer.ContentDigest([]byte("other")),
		"sha-512 only":    "sha-512=:WZDPaVn/7XgHaAy8pmojAkGWoRx2UFChF41A2svX+TaPm+AbwAgBWnrIiYllu7BNNyealdVLvRwEmTHWXvJwew==:",
		"sha-512 differs": want + ", sha-512=:AA==:",
		"not bytes":       `sha-256="x"`,
		"malformed":       "sha-256=",
	} {
		if err := corepeer.CheckContentDigest(value, body); !errors.Is(err, corepeer.ErrDigest) {
			t.Errorf("%s: %v, want ErrDigest", name, err)
		}
	}

	// A streamed answer's digest is read for the SHA-256 the reader then
	// computes.
	if got, err := corepeer.DigestSHA256(both); err != nil || !bytes.Equal(got, sum[:]) {
		t.Errorf("DigestSHA256 = %x, %v", got, err)
	}
	for name, value := range map[string]string{
		"sha-512 only": "sha-512=:AA==:",
		"not bytes":    `sha-256="x"`,
		"short":        "sha-256=:AA==:",
		"malformed":    "sha-256=",
	} {
		if _, err := corepeer.DigestSHA256(value); !errors.Is(err, corepeer.ErrDigest) {
			t.Errorf("DigestSHA256 %s: %v, want ErrDigest", name, err)
		}
	}
}

// TestUnitVersion parses <major>.<minor> and judges compatibility by the
// major version only.
func TestUnitVersion(t *testing.T) {
	if corepeer.CurrentVersion.String() != "1.0" || !corepeer.CurrentVersion.Compatible() {
		t.Errorf("CurrentVersion = %s", corepeer.CurrentVersion)
	}
	v, err := corepeer.ParseVersion("1.12")
	if err != nil || v != (corepeer.Version{Major: 1, Minor: 12}) || !v.Compatible() {
		t.Errorf("ParseVersion(1.12) = %+v, %v", v, err)
	}
	if v, _ := corepeer.ParseVersion("2.0"); v.Compatible() {
		t.Error("2.0 is compatible")
	}
	for _, bad := range []string{"", "1", "1.", ".1", "+1.0", "1.-1", "a.b", "1.0.0", "1234567890.0"} {
		if _, err := corepeer.ParseVersion(bad); !errors.Is(err, corepeer.ErrVersion) {
			t.Errorf("ParseVersion(%q): %v", bad, err)
		}
	}
}

// TestUnitCallerContext carries the verified caller.
func TestUnitCallerContext(t *testing.T) {
	if _, ok := corepeer.Caller(context.Background()); ok {
		t.Error("a bare context has a caller")
	}
	p, ok := corepeer.Caller(corepeer.WithCaller(context.Background(), model.Peer{InstanceID: callerID}))
	if !ok || p.InstanceID != callerID {
		t.Errorf("Caller = %+v, %v", p, ok)
	}
}
