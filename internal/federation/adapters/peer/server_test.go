package peer_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const (
	callerID = "jfrt@00000001-0000-4000-8000-000000000001"
	serverID = "jfrt@00000002-0000-4000-8000-000000000002"
	origin   = "http://example.com"
	path     = peeradapter.Prefix + "/streams/repo%3Alibs/events"
)

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func key(seed byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	priv := ed25519.NewKeyFromSeed(s)
	return priv.Public().(ed25519.PublicKey), priv //nolint:forcetypeassert // ed25519 public keys are this type
}

// fixture is one receiving instance: its peer store holding the caller, its
// middleware, and a handler that counts the requests that reach it.
type fixture struct {
	store   *fedinmem.Store
	now     atomic.Int64
	server  *peeradapter.Server
	caller  corepeer.Signer
	calls   atomic.Int32
	seen    atomic.Pointer[model.Peer]
	handler http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{store: fedinmem.New(memtx.New())}
	f.now.Store(t0.UnixNano())
	pub, priv := key(1)
	if err := f.store.Peers().Put(t.Context(), model.Peer{
		InstanceID: callerID, BaseURL: "https://b.example.com", PublicKey: pub,
		ProtocolVersion: instance.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	f.caller = corepeer.Signer{KeyID: callerID, Key: priv, Now: f.clock}
	_, serverKey := key(2)
	f.server = &peeradapter.Server{
		Verifier: &corepeer.Verifier{Keys: f.store.Peers(), Nonces: f.store.RequestNonces(), Now: f.clock},
		Signer: func(context.Context) (corepeer.Signer, error) {
			return corepeer.Signer{KeyID: serverID, Key: serverKey, Now: f.clock}, nil
		},
	}
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if p, ok := corepeer.Caller(r.Context()); ok {
			f.seen.Store(&p)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"applied_seq":`+itoa(len(body))+`}`)
	})
	return f
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (f *fixture) clock() time.Time { return time.Unix(0, f.now.Load()).UTC() }

// request builds a request the caller signed for origin+target.
func (f *fixture) request(t *testing.T, method, target string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, origin+target, bytes.NewReader(body))
	if err := f.caller.SignRequest(method, origin+target, r.Header, body); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.server.Middleware(f.handler).ServeHTTP(w, r)
	return w
}

// verifyAnswer checks the answer's signature against the server's key and
// the request it answers.
func verifyAnswer(t *testing.T, f *fixture, r *http.Request, w *httptest.ResponseRecorder) error {
	t.Helper()
	serverPub, _ := key(2)
	m := corepeer.Message{
		Status: w.Code, Header: w.Header(),
		Request: &corepeer.Message{Method: r.Method, TargetURI: peeradapter.TargetURI(r), Header: r.Header},
	}
	return corepeer.VerifyResponse(m, w.Body.Bytes(), serverID, serverPub, f.clock()) //nolint:wrapcheck // the test reads the verdict
}

// TestUnitMiddlewareServesVerified passes a verified request to the
// handler once, with the caller in its context, and signs the answer.
func TestUnitMiddlewareServesVerified(t *testing.T) {
	f := newFixture(t)
	r := f.request(t, http.MethodPost, path+"?x=1", []byte(`{"events":[]}`))
	w := f.serve(r)
	if w.Code != http.StatusOK || f.calls.Load() != 1 {
		t.Fatalf("status %d, %d calls, body %s", w.Code, f.calls.Load(), w.Body)
	}
	if p := f.seen.Load(); p == nil || p.InstanceID != callerID {
		t.Errorf("the handler saw caller %+v", p)
	}
	if err := verifyAnswer(t, f, r, w); err != nil {
		t.Errorf("the answer does not verify: %v", err)
	}
	if w.Header().Get(corepeer.HeaderProtocol) != "1.0" || w.Header().Get("Content-Length") != itoa(w.Body.Len()) {
		t.Errorf("answer headers %v", w.Header())
	}
}

// TestUnitMiddlewareRefusals sends each refused request and checks that the
// handler never ran, the status and flat envelope, and that the answer
// repeats no signature or nonce.
func TestUnitMiddlewareRefusals(t *testing.T) {
	body := []byte(`{"events":[]}`)
	cases := []struct {
		name   string
		build  func(*testing.T, *fixture) *http.Request
		status int
		code   string
	}{
		{"unknown key", func(t *testing.T, f *fixture) *http.Request {
			_ = f.store.Peers().Delete(t.Context(), callerID)
			return f.request(t, http.MethodPost, path, body)
		}, 401, "UNAUTHORIZED"},
		{"bad signature", func(t *testing.T, f *fixture) *http.Request {
			_, other := key(9)
			f.caller.Key = other
			return f.request(t, http.MethodPost, path, body)
		}, 401, "UNAUTHORIZED"},
		{"altered body", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Body = io.NopCloser(strings.NewReader(`{"events":[1]}`))
			return r
		}, 401, "UNAUTHORIZED"},
		{"altered body and digest", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Body = io.NopCloser(strings.NewReader(`{"events":[1]}`))
			r.Header.Set(corepeer.HeaderContentDigest, corepeer.ContentDigest([]byte(`{"events":[1]}`)))
			return r
		}, 401, "UNAUTHORIZED"},
		{"altered path", func(t *testing.T, f *fixture) *http.Request {
			signed := f.request(t, http.MethodPost, path, body)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
				origin+strings.Replace(path, "libs", "other", 1), bytes.NewReader(body))
			r.Header = signed.Header
			return r
		}, 401, "UNAUTHORIZED"},
		{"skew over five minutes", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			f.now.Add(int64(5*time.Minute + time.Second))
			return r
		}, 401, "UNAUTHORIZED"},
		{"replayed nonce", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			first := r.Clone(t.Context())
			first.Body = io.NopCloser(bytes.NewReader(body))
			if w := f.serve(first); w.Code != http.StatusOK {
				t.Fatalf("the first request answered %d", w.Code)
			}
			f.calls.Store(0)
			return r
		}, 401, "UNAUTHORIZED"},
		{"no Signature", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Header.Del(corepeer.HeaderSignature)
			return r
		}, 401, "UNAUTHORIZED"},
		{"no Signature-Input", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Header.Del(corepeer.HeaderSignatureInput)
			return r
		}, 401, "UNAUTHORIZED"},
		{"no Date", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Header.Del(corepeer.HeaderDate)
			return r
		}, 401, "UNAUTHORIZED"},
		{"no Content-Digest", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Header.Del(corepeer.HeaderContentDigest)
			return r
		}, 401, "UNAUTHORIZED"},
		{"no signature headers at all", func(t *testing.T, f *fixture) *http.Request {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, origin+path, nil)
			r.Header.Set(corepeer.HeaderProtocol, "1.0")
			return r
		}, 401, "UNAUTHORIZED"},
		{"no protocol version", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Header.Del(corepeer.HeaderProtocol)
			return r
		}, 400, "BAD_REQUEST"},
		{"major version mismatch, unverified", func(t *testing.T, f *fixture) *http.Request {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, origin+path, nil)
			r.Header.Set(corepeer.HeaderProtocol, "2.0")
			return r
		}, 400, "BAD_REQUEST"},
		{"body over its bound", func(t *testing.T, f *fixture) *http.Request {
			f.server.MaxRequestBody = 4
			return f.request(t, http.MethodPost, path, body)
		}, 413, "REQUEST_ENTITY_TOO_LARGE"},
		{"unreadable body", func(t *testing.T, f *fixture) *http.Request {
			r := f.request(t, http.MethodPost, path, body)
			r.Body = io.NopCloser(errReader{})
			return r
		}, 400, "BAD_REQUEST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			r := c.build(t, f)
			w := f.serve(r)
			if f.calls.Load() != 0 {
				t.Errorf("the handler ran %d times", f.calls.Load())
			}
			if w.Code != c.status {
				t.Errorf("status %d, want %d; body %s", w.Code, c.status, w.Body)
			}
			var got struct{ Code, Message string }
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Code != c.code || got.Message == "" {
				t.Errorf("body %s, want the flat envelope with %s", w.Body, c.code)
			}
			if w.Header().Get(corepeer.HeaderSignature) != "" {
				t.Error("a refusal of an unverified request is signed")
			}
			if s, err := corepeer.ParseSignature(r.Header); err == nil {
				for _, secret := range []string{s.Params.Nonce, r.Header.Get(corepeer.HeaderSignature)} {
					if strings.Contains(w.Body.String(), secret) {
						t.Errorf("the answer %s repeats a nonce or signature", w.Body)
					}
				}
			}
			if p, _ := f.store.Peers().Get(t.Context(), callerID); p.InstanceID != "" && !p.Compatible() {
				t.Errorf("an unverified request changed the peer record to %+v", p)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestUnitMiddlewareVersionMismatch answers a verified request with another
// major version 400, signed, records the version on the caller's record so
// status shows it unavailable, and clears it once the caller speaks this
// build's version again. A minor difference is accepted.
func TestUnitMiddlewareVersionMismatch(t *testing.T) {
	f := newFixture(t)
	r := f.request(t, http.MethodGet, path, nil)
	r.Header.Set(corepeer.HeaderProtocol, "2.0")
	// The protocol header is not covered, so changing it leaves the
	// signature valid: what a peer of another major version sends.
	w := f.serve(r)
	if w.Code != http.StatusBadRequest || f.calls.Load() != 0 {
		t.Fatalf("status %d, %d calls", w.Code, f.calls.Load())
	}
	if err := verifyAnswer(t, f, r, w); err != nil {
		t.Errorf("the 400 to a verified caller is not signed: %v", err)
	}
	if !strings.Contains(w.Body.String(), "2.0") || !strings.Contains(w.Body.String(), "1.0") {
		t.Errorf("body %s does not name both versions", w.Body)
	}
	p, err := f.store.Peers().Get(t.Context(), callerID)
	if err != nil || p.ProtocolVersion != 2 || p.DeriveStatus(f.clock(), time.Hour, false, 0) != model.PeerOffline {
		t.Errorf("the record after a mismatch is %+v, %v", p, err)
	}

	r = f.request(t, http.MethodGet, path, nil)
	r.Header.Set(corepeer.HeaderProtocol, "1.9")
	if w := f.serve(r); w.Code != http.StatusOK {
		t.Fatalf("a minor difference answered %d", w.Code)
	}
	if p, _ := f.store.Peers().Get(t.Context(), callerID); !p.Compatible() {
		t.Errorf("the record after a compatible request is %+v", p)
	}
}

// TestUnitMiddlewarePairBootstrap verifies POST /pair with the key the body
// presents, since the caller is not yet a peer.
func TestUnitMiddlewarePairBootstrap(t *testing.T) {
	pub, priv := key(7)
	stranger := "jfrt@00000007-0000-4000-8000-000000000007"
	pairBody := func(id string, pubKey []byte, version string) []byte {
		b, _ := json.Marshal(map[string]string{
			"nonce": "n", "instance_id": id, "base_url": "https://c.example.com",
			"public_key": base64.StdEncoding.EncodeToString(pubKey), "protocol_version": version,
		})
		return b
	}
	for _, c := range []struct {
		name   string
		body   []byte
		signer string
		status int
	}{
		{"presented key", pairBody(stranger, pub, "1.0"), stranger, 200},
		{"keyid is not instance_id", pairBody(stranger, pub, "1.0"), callerID, 401},
		{"another key", pairBody(stranger, make([]byte, 32), "1.0"), stranger, 401},
		{"not JSON", []byte(`{`), stranger, 400},
		{"key not base64", []byte(`{"instance_id":"x","public_key":"!"}`), stranger, 400},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.caller = corepeer.Signer{KeyID: c.signer, Key: priv, Now: f.clock}
			r := f.request(t, http.MethodPost, peeradapter.Prefix+"/pair", c.body)
			w := f.serve(r)
			if w.Code != c.status {
				t.Fatalf("status %d, want %d: %s", w.Code, c.status, w.Body)
			}
			if c.status != 200 {
				return
			}
			p := f.seen.Load()
			if p == nil || p.InstanceID != stranger || !p.PublicKey.Equal(pub) || p.BaseURL != "https://c.example.com" ||
				p.ProtocolVersion != 1 {
				t.Errorf("the handler saw %+v", p)
			}
			if err := verifyAnswer(t, f, r, w); err != nil {
				t.Errorf("the pairing answer does not verify: %v", err)
			}
			if _, err := f.store.Peers().Get(t.Context(), stranger); err == nil {
				t.Error("the middleware recorded the stranger as a peer")
			}
		})
	}
}

// TestUnitMiddlewareStreamsPresetDigest signs an answer whose handler sets
// Content-Digest first before its body is written, and streams it.
func TestUnitMiddlewareStreamsPresetDigest(t *testing.T) {
	f := newFixture(t)
	blob := bytes.Repeat([]byte("blob"), 1024)
	sum := sha256.Sum256(blob)
	var flushedBeforeEnd atomic.Bool
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(corepeer.HeaderContentDigest, corepeer.DigestFromSHA256(sum[:]))
		_, _ = w.Write(blob[:10])
		w.(http.Flusher).Flush() //nolint:forcetypeassert // the middleware's writer flushes
		flushedBeforeEnd.Store(true)
		_, _ = w.Write(blob[10:])
		w.WriteHeader(http.StatusTeapot) // ignored once the status is out
	})
	r := f.request(t, http.MethodGet, peeradapter.Prefix+"/blobs/x", nil)
	w := f.serve(r)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), blob) || !flushedBeforeEnd.Load() || !w.Flushed {
		t.Fatalf("status %d, %d bytes, flushed %v", w.Code, w.Body.Len(), w.Flushed)
	}
	if err := verifyAnswer(t, f, r, w); err != nil {
		t.Errorf("the streamed answer does not verify: %v", err)
	}
}

// TestUnitMiddlewareEmptyAnswer signs an answer whose handler writes nothing.
func TestUnitMiddlewareEmptyAnswer(t *testing.T) {
	f := newFixture(t)
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush() //nolint:forcetypeassert // a buffered answer ignores it
	})
	r := f.request(t, http.MethodDelete, peeradapter.Prefix+"/pair?use_case=federated-repo%2F%2A", nil)
	w := f.serve(r)
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("status %d, body %q", w.Code, w.Body)
	}
	if err := verifyAnswer(t, f, r, w); err != nil {
		t.Errorf("the empty answer does not verify: %v", err)
	}
}

type brokenKeys struct{ err error }

func (b brokenKeys) Get(context.Context, string) (model.Peer, error) { return model.Peer{}, b.err }
func (b brokenKeys) SetProtocolVersion(context.Context, string, int) error {
	return b.err
}

// TestUnitMiddlewareFailures answers 500, and runs no handler, when the
// stores or the instance key fail; and an answer it cannot sign is a 500,
// not an unsigned success.
func TestUnitMiddlewareFailures(t *testing.T) {
	errDown := errors.New("store down")
	f := newFixture(t)
	f.server.Verifier.Keys = brokenKeys{errDown}
	if w := f.serve(f.request(t, http.MethodGet, path, nil)); w.Code != 500 || f.calls.Load() != 0 {
		t.Errorf("a failed peer store: %d", w.Code)
	}

	f = newFixture(t)
	f.server.Verifier.Keys = recordFails{f.store.Peers(), errDown}
	r := f.request(t, http.MethodGet, path, nil)
	r.Header.Set(corepeer.HeaderProtocol, "3.1")
	if w := f.serve(r); w.Code != 500 || f.calls.Load() != 0 {
		t.Errorf("a failed version record: %d", w.Code)
	}

	f = newFixture(t)
	f.server.Signer = func(context.Context) (corepeer.Signer, error) { return corepeer.Signer{}, errDown }
	if w := f.serve(f.request(t, http.MethodGet, path, nil)); w.Code != 500 || f.calls.Load() != 0 {
		t.Errorf("no instance key: %d", w.Code)
	}

	for _, preset := range []bool{false, true} {
		f = newFixture(t)
		f.server.Signer = func(context.Context) (corepeer.Signer, error) { return corepeer.Signer{KeyID: "x"}, nil }
		f.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if preset {
				w.Header().Set(corepeer.HeaderContentDigest, corepeer.ContentDigest(nil))
			}
			if _, err := w.Write([]byte("secret")); preset && err == nil {
				t.Error("a write after a failed signature succeeded")
			}
		})
		w := f.serve(f.request(t, http.MethodGet, path, nil))
		if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || w.Header().Get(corepeer.HeaderContentDigest) != "" {
			t.Errorf("an unsignable answer (preset %v): %d %s", preset, w.Code, w.Body)
		}
	}
}

type recordFails struct {
	corepeer.PeerKeys
	err error
}

func (r recordFails) SetProtocolVersion(context.Context, string, int) error { return r.err }

// TestUnitTargetURI reconstructs the URI a peer signed, through a proxy's
// forwarded headers or from the connection.
func TestUnitTargetURI(t *testing.T) {
	for _, c := range []struct {
		name   string
		header map[string]string
		tls    bool
		want   string
	}{
		{"direct", nil, false, "http://a.example.com:8081/p?q=1"},
		{"direct TLS", nil, true, "https://a.example.com:8081/p?q=1"},
		{
			"forwarded",
			map[string]string{"X-Forwarded-Host": "edge.example.com, inner", "X-Forwarded-Proto": "https"},
			false,
			"https://edge.example.com/p?q=1",
		},
		{
			"forwarded port",
			map[string]string{"X-Forwarded-Host": "edge.example.com", "X-Forwarded-Proto": "https", "X-Forwarded-Port": "8443"},
			false, "https://edge.example.com:8443/p?q=1",
		},
		{
			"forwarded default port",
			map[string]string{"X-Forwarded-Host": "edge.example.com", "X-Forwarded-Port": "80"},
			false, "http://edge.example.com/p?q=1",
		},
		{
			"forwarded host with port",
			map[string]string{"X-Forwarded-Host": "edge.example.com:9", "X-Forwarded-Port": "10"},
			false, "http://edge.example.com:9/p?q=1",
		},
		{
			"bad forwarded proto",
			map[string]string{"X-Forwarded-Host": "edge.example.com", "X-Forwarded-Proto": "gopher"},
			true, "https://edge.example.com/p?q=1",
		},
		{
			"override ignored",
			map[string]string{"X-JFrog-Override-Base-Url": "https://other/artifactory"},
			false,
			"http://a.example.com:8081/p?q=1",
		},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://a.example.com:8081/p?q=1", nil)
		for k, v := range c.header {
			r.Header.Set(k, v)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := peeradapter.TargetURI(r); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://h/a%2Fb", nil)
	r.RequestURI = ""
	if got := peeradapter.TargetURI(r); got != "http://h/a%2Fb" {
		t.Errorf("a request without RequestURI: %s", got)
	}
}
