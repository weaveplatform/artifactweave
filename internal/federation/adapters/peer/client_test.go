package peer_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// remote is a peer instance behind a TLS listener: the middleware over a
// handler, and in front of it a hook that may answer or tamper first.
type remote struct {
	ts      *httptest.Server
	calls   atomic.Int32
	before  func(w http.ResponseWriter, r *http.Request) bool
	tamper  func(*httptest.ResponseRecorder)
	handler http.HandlerFunc
	peer    model.Peer
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	store := fedinmem.New(memtx.New())
	callerPub, _ := key(1)
	if err := store.Peers().Put(t.Context(), model.Peer{
		InstanceID: callerID, PublicKey: callerPub, ProtocolVersion: instance.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	serverPub, serverPriv := key(2)
	rm := &remote{}
	rm.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"instance_id":"`+serverID+`","protocol_version":"1.0","version":"1","time":"2026-11-24T12:00:00Z","use_cases":[]}`)
	}
	server := &peeradapter.Server{
		Verifier: &corepeer.Verifier{Keys: store.Peers(), Nonces: store.RequestNonces()},
		Signer: func(context.Context) (corepeer.Signer, error) {
			return corepeer.Signer{KeyID: serverID, Key: serverPriv}, nil
		},
	}
	guarded := server.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rm.calls.Add(1)
		rm.handler(w, r)
	}))
	rm.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rm.before != nil && rm.before(w, r) {
			return
		}
		if rm.tamper == nil {
			guarded.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, r)
		rm.tamper(rec)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(rm.ts.Close)
	rm.peer = model.Peer{InstanceID: serverID, BaseURL: rm.ts.URL, PublicKey: serverPub}
	return rm
}

func (rm *remote) factory(t *testing.T, o peeradapter.ClientOptions) *peeradapter.Factory {
	t.Helper()
	_, callerPriv := key(1)
	if o.Signer == nil {
		o.Signer = func(context.Context) (corepeer.Signer, error) {
			return corepeer.Signer{KeyID: callerID, Key: callerPriv}, nil
		}
	}
	if o.RootCAs == nil {
		o.RootCAs = x509.NewCertPool()
		o.RootCAs.AddCert(rm.ts.Certificate())
	}
	o.AllowedPrivateOrigins = append(o.AllowedPrivateOrigins, rm.ts.URL)
	if o.Backoff == 0 {
		o.Backoff = time.Millisecond
	}
	f, err := peeradapter.NewFactory(o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (rm *remote) client(t *testing.T, o peeradapter.ClientOptions) *peeradapter.Client {
	t.Helper()
	c, err := rm.factory(t, o).Client(t.Context(), rm.peer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// TestUnitClientRoundTrip calls a peer through the generated client: the
// request is signed and verified there, and the signed answer verified
// here, over TLS whose certificate is verified.
func TestUnitClientRoundTrip(t *testing.T) {
	rm := newRemote(t)
	api, err := rm.client(t, peeradapter.ClientOptions{}).API()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := api.GetPeerStatusWithResponse(t.Context(), &peerclient.GetPeerStatusParams{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.InstanceId != serverID {
		t.Fatalf("status %d, body %s", resp.StatusCode(), resp.Body)
	}
	if rm.calls.Load() != 1 {
		t.Errorf("the handler ran %d times", rm.calls.Load())
	}
	// A body travels and is digested both ways.
	rm.handler = func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}
	push, err := api.PushEventsWithBodyWithResponse(t.Context(), "repo:libs", &peerclient.PushEventsParams{},
		"application/json", strings.NewReader(`{"origin":"x","from_seq":1,"events":[]}`))
	if err != nil || string(push.Body) != `{"origin":"x","from_seq":1,"events":[]}` {
		t.Fatalf("push: %v, %s", err, push.Body)
	}
}

// TestUnitClientTLSVerified refuses a peer whose certificate this instance
// does not trust: signatures add to TLS, they do not replace it.
func TestUnitClientTLSVerified(t *testing.T) {
	rm := newRemote(t)
	c := rm.client(t, peeradapter.ClientOptions{RootCAs: x509.NewCertPool()})
	_, err := c.Do(statusRequest(t, rm))
	if err == nil || rm.calls.Load() != 0 {
		t.Fatalf("an untrusted certificate: %v, %d calls", err, rm.calls.Load())
	}
}

func statusRequest(t *testing.T, rm *remote) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rm.ts.URL+peeradapter.Prefix+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestUnitClientRejectsTamperedAnswer refuses an answer whose body, status
// or signature was changed after the peer signed it, or that was signed by
// another key.
func TestUnitClientRejectsTamperedAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		tamper func(*httptest.ResponseRecorder)
		want   error
	}{
		"body": {func(r *httptest.ResponseRecorder) {
			b := bytes.Replace(r.Body.Bytes(), []byte(serverID), []byte(callerID), 1)
			r.Body = bytes.NewBuffer(b)
			r.Header().Del("Content-Length")
		}, corepeer.ErrDigest},
		"body and digest": {func(r *httptest.ResponseRecorder) {
			r.Body = bytes.NewBufferString(`{}`)
			r.Header().Set(corepeer.HeaderContentDigest, corepeer.ContentDigest([]byte(`{}`)))
			r.Header().Del("Content-Length")
		}, corepeer.ErrBadSignature},
		"status": {func(r *httptest.ResponseRecorder) { r.Code = http.StatusCreated }, corepeer.ErrBadSignature},
		"signature removed": {func(r *httptest.ResponseRecorder) {
			r.Header().Del(corepeer.HeaderSignature)
		}, corepeer.ErrUnsigned},
	} {
		t.Run(name, func(t *testing.T) {
			rm := newRemote(t)
			rm.tamper = c.tamper
			resp, err := rm.client(t, peeradapter.ClientOptions{}).Do(statusRequest(t, rm))
			var unverified *peeradapter.UnverifiedAnswerError
			if resp != nil || !errors.As(err, &unverified) || !errors.Is(err, c.want) {
				t.Fatalf("Do = %v, %v; want an UnverifiedAnswerError wrapping %v", resp, err, c.want)
			}
			if !strings.Contains(err.Error(), "not signed by the peer") {
				t.Errorf("error text %q", err)
			}
		})
	}
	rm := newRemote(t)
	other, _ := key(5)
	rm.peer.PublicKey = other
	if _, err := rm.client(t, peeradapter.ClientOptions{}).Do(statusRequest(t, rm)); !errors.Is(err, corepeer.ErrBadSignature) {
		t.Errorf("an answer signed by another key: %v", err)
	}
	rm.peer.PublicKey = nil
	if _, err := rm.client(t, peeradapter.ClientOptions{}).Do(statusRequest(t, rm)); !errors.Is(err, peeradapter.ErrNoPeerKey) {
		t.Errorf("no key to verify with: %v", err)
	}
}

// TestUnitClientRetries retries a safe or natural operation after a
// transient answer, signing each attempt afresh (the peer refuses a
// replayed nonce), and sends any other request once.
func TestUnitClientRetries(t *testing.T) {
	rm := newRemote(t)
	var unavailable atomic.Int32
	unavailable.Store(2)
	attempts := atomic.Int32{}
	rm.before = func(w http.ResponseWriter, _ *http.Request) bool {
		attempts.Add(1)
		if unavailable.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	resp, err := rm.client(t, peeradapter.ClientOptions{}).Do(statusRequest(t, rm))
	if err != nil || resp.StatusCode != http.StatusOK || attempts.Load() != 3 || rm.calls.Load() != 1 {
		t.Fatalf("Do = %v, %v after %d attempts", resp, err, attempts.Load())
	}

	// Exhausted: the last transient answer is returned as what arrived.
	unavailable.Store(5)
	attempts.Store(0)
	_, err = rm.client(t, peeradapter.ClientOptions{Attempts: 2}).Do(statusRequest(t, rm))
	var unverified *peeradapter.UnverifiedAnswerError
	if !errors.As(err, &unverified) || unverified.Status != http.StatusServiceUnavailable || attempts.Load() != 2 {
		t.Errorf("exhausted retries: %v after %d attempts", err, attempts.Load())
	}

	// A path no operation names is not known to be retry-safe.
	unavailable.Store(5)
	attempts.Store(0)
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, rm.ts.URL+peeradapter.Prefix+"/unknown", strings.NewReader("{}"))
	if _, err := rm.client(t, peeradapter.ClientOptions{}).Do(r); err == nil || attempts.Load() != 1 {
		t.Errorf("an unknown operation was tried %d times: %v", attempts.Load(), err)
	}

	// Cancellation during the back-off ends the call.
	unavailable.Store(5)
	ctx, cancel := context.WithCancel(t.Context())
	rm.before = func(w http.ResponseWriter, _ *http.Request) bool {
		cancel()
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	r, _ = http.NewRequestWithContext(ctx, http.MethodGet, rm.ts.URL+peeradapter.Prefix+"/status", nil)
	if _, err := rm.client(t, peeradapter.ClientOptions{Backoff: time.Hour}).Do(r); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled during back-off: %v", err)
	}
}

// TestUnitClientConnectionFailure retries a refused connection and then
// reports it.
func TestUnitClientConnectionFailure(t *testing.T) {
	rm := newRemote(t)
	c := rm.client(t, peeradapter.ClientOptions{})
	rm.ts.Close()
	if _, err := c.Do(statusRequest(t, rm)); !errors.Is(err, outbound.ErrConnection) {
		t.Errorf("a closed peer: %v", err)
	}
}

// TestUnitClientBounds refuses bodies over their bounds both ways.
func TestUnitClientBounds(t *testing.T) {
	rm := newRemote(t)
	if _, err := rm.client(t, peeradapter.ClientOptions{MaxResponseBody: 8}).Do(statusRequest(t, rm)); !errors.Is(err, peeradapter.ErrTooLarge) {
		t.Errorf("a large answer: %v", err)
	}
	rm.calls.Store(0)
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, rm.ts.URL+peeradapter.Prefix+"/streams/s/events",
		strings.NewReader(`{"events":[]}`))
	if _, err := rm.client(t, peeradapter.ClientOptions{MaxRequestBody: 4}).Do(r); !errors.Is(err, peeradapter.ErrTooLarge) {
		t.Errorf("a large request: %v", err)
	}
	r, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, rm.ts.URL+peeradapter.Prefix+"/streams/s/events",
		io.NopCloser(errBody{}))
	if _, err := rm.client(t, peeradapter.ClientOptions{}).Do(r); err == nil {
		t.Error("an unreadable request body was sent")
	}
	if rm.calls.Load() != 0 {
		t.Errorf("the handler ran %d times", rm.calls.Load())
	}
}

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

// TestUnitClientPairAnswerKey verifies the answer to POST /pair with the
// key the answer presents, before any record of the issuer exists.
func TestUnitClientPairAnswerKey(t *testing.T) {
	rm := newRemote(t)
	serverPub, _ := key(2)
	rm.handler = func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"instance_id": serverID, "public_key": base64.StdEncoding.EncodeToString(serverPub),
			"base_url": rm.ts.URL, "protocol_version": "1.0", "use_case": "federated-repo/*",
		})
	}
	f := rm.factory(t, peeradapter.ClientOptions{})
	c, err := f.Client(t.Context(), model.Peer{InstanceID: serverID, BaseURL: rm.ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	callerPub, _ := key(1)
	redeem, _ := json.Marshal(map[string]string{
		"nonce": "n", "instance_id": callerID, "base_url": "https://b.example.com",
		"public_key": base64.StdEncoding.EncodeToString(callerPub), "protocol_version": "1.0",
	})
	pair := func(c *peeradapter.Client) error {
		r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, rm.ts.URL+peeradapter.Prefix+"/pair",
			bytes.NewReader(redeem))
		resp, err := c.Do(r)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := pair(c.WithAnswerKey(peeradapter.PairAnswerKey)); err != nil {
		t.Errorf("a pairing answer with its own key: %v", err)
	}
	if err := pair(c); !errors.Is(err, peeradapter.ErrNoPeerKey) {
		t.Errorf("a pairing answer without WithAnswerKey: %v", err)
	}
	other, _ := key(6)
	for name, body := range map[string]string{
		"another key": `{"instance_id":"` + serverID + `","public_key":"` + base64.StdEncoding.EncodeToString(other) + `"}`,
		"no key":      `{"instance_id":"` + serverID + `"}`,
		"not JSON":    `[`,
	} {
		rm.handler = func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
		if err := pair(c.WithAnswerKey(peeradapter.PairAnswerKey)); err == nil {
			t.Errorf("%s: a pairing answer verified", name)
		}
	}
}

// TestUnitClientConfiguration refuses configuration it cannot use.
func TestUnitClientConfiguration(t *testing.T) {
	if _, err := peeradapter.NewFactory(peeradapter.ClientOptions{}); err == nil {
		t.Error("a factory without a signer")
	}
	signer := func(context.Context) (corepeer.Signer, error) { return corepeer.Signer{}, errors.New("no key") }
	if _, err := peeradapter.NewFactory(peeradapter.ClientOptions{Signer: signer, AllowedPrivateOrigins: []string{"::"}}); err == nil {
		t.Error("a factory with a malformed private origin")
	}
	f, err := peeradapter.NewFactory(peeradapter.ClientOptions{Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", "ftp://h", "::"} {
		if _, err := f.Client(t.Context(), model.Peer{BaseURL: base}); err == nil {
			t.Errorf("a client for base URL %q", base)
		}
	}
	c, err := f.Client(t.Context(), model.Peer{BaseURL: "https://peer.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://peer.example.com/x", nil)
	if _, err := c.Do(r); err == nil {
		t.Error("a request without the instance key")
	}
	proxyErr := errors.New("two default proxies")
	f, _ = peeradapter.NewFactory(peeradapter.ClientOptions{
		Signer: signer,
		Proxy:  func(context.Context) (*url.URL, *outbound.NTLMProxy, error) { return nil, nil, proxyErr },
	})
	if _, err := f.Client(t.Context(), model.Peer{BaseURL: "https://peer.example.com"}); !errors.Is(err, proxyErr) {
		t.Errorf("a failed proxy lookup: %v", err)
	}
	f, _ = peeradapter.NewFactory(peeradapter.ClientOptions{
		Signer: signer,
		Proxy: func(context.Context) (*url.URL, *outbound.NTLMProxy, error) {
			return &url.URL{Scheme: "http", Host: "proxy.example.com:3128", Path: "/x"}, nil, nil
		},
	})
	if _, err := f.Client(t.Context(), model.Peer{BaseURL: "https://peer.example.com"}); err == nil {
		t.Error("a proxy URL with a path")
	}
}
