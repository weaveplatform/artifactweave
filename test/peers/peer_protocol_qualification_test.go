package peers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccPeerProtocolQualificationSQLite(t *testing.T) {
	t.Parallel()
	testPeerProtocolQualification(t, peers.SQLite)
}

func TestAccPeerProtocolQualificationPostgres(t *testing.T) {
	t.Parallel()
	testPeerProtocolQualification(t, peers.Postgres)
}

// The receivers are three app.Run OS processes, with their actual stored
// identities. The calling production clients run in this test process using
// those identities. The proxy below changes an actual process's answer only
// after checking its signature; it implements no peer endpoint or signing.
func testPeerProtocolQualification(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver, PeerProbeInterval: probeInterval})
	w := &protocolQualification{t: t, cluster: c}
	for _, in := range c.Instances {
		id := in.Identity(t)
		w.identities = append(w.identities, id)
		for _, key := range [][]byte{id.PrivateKey, id.PrivateKey.Seed()} {
			w.secrets = append(w.secrets, base64.StdEncoding.EncodeToString(key),
				base64.RawURLEncoding.EncodeToString(key), hex.EncodeToString(key))
		}
	}
	// Check after the processes drain their logs, before Start's failure-log
	// cleanup can print anything. Canary values never appear in diagnostics.
	t.Cleanup(func() {
		c.Close()
		w.checkLogs()
		if left := c.Leftovers(context.Background()); len(left) != 0 {
			t.Errorf("protocol fixture retained %d resources", len(left))
		}
	})
	for i := range c.Instances {
		for j := i + 1; j < len(c.Instances); j++ {
			w.pair(i, j, "federated-repo/*")
		}
	}
	w.pair(0, 1, "federated-repo/r")
	w.pairings("federated-repo/*", [][]int{{1, 2}, {0, 2}, {0, 1}})
	w.pairings("federated-repo/r", [][]int{{1}, {0}, {}})
	// Each process's own periodic client must obtain and verify a fresh
	// status answer from both other processes. LastSeen on push peers is
	// written by the successful outgoing probe, not by an incoming read.
	probeSince := time.Now()
	for i, in := range c.Instances {
		for j, remote := range w.identities {
			if i != j {
				awaitPeer(t, in, remote.InstanceID, "a verified process-originated status probe", seenSince(probeSince))
			}
		}
	}
	// Isolate the controlled version changes from concurrent status probes.
	// The harness's fault-injection clients still reach the real ingress.
	for i := range c.Instances {
		for j := i + 1; j < len(c.Instances); j++ {
			c.Partition(i, j)
		}
	}

	for i := range c.Instances {
		for j := range c.Instances {
			if i == j {
				continue
			}
			t.Run(c.Instances[i].Name+"_to_"+c.Instances[j].Name, func(t *testing.T) {
				w.exchange(t, i, j, false)
				w.refusals(t, i, j)
				w.exchange(t, i, j, true)
			})
		}
	}
	for i := range c.Instances {
		for j := i + 1; j < len(c.Instances); j++ {
			c.Heal(i, j)
		}
	}

	const canceled = `{"message":"Trust with service federated-repo was canceled successfully"}`
	w.admin(1, http.MethodDelete, stPairing+"federated-repo/r", `{"base_url":"`+c.Instances[0].URL+`"}`, canceled)
	w.pairings("federated-repo/r", [][]int{{}, {}, {}})
	w.pairings("federated-repo/*", [][]int{{1, 2}, {0, 2}, {0, 1}})
	w.admin(0, http.MethodDelete, stPairing+"federated-repo/*", `{"base_url":"`+c.Instances[1].URL+`"}`, canceled)
	w.pairings("federated-repo/*", [][]int{{2}, {2}, {0, 1}})
	for _, edge := range [][2]int{{0, 2}, {2, 0}, {1, 2}, {2, 1}} {
		w.exchange(t, edge[0], edge[1], false)
	}
}

type protocolQualification struct {
	t          *testing.T
	cluster    *peers.Cluster
	identities []instance.InstanceIdentity
	secrets    []string
	refused    []string
}

func (w *protocolQualification) admin(i int, method, path, body, want string) string {
	w.t.Helper()
	status, data := w.cluster.Instances[i].Do(w.t, method, path, body)
	if status != http.StatusOK || want != "" && strings.TrimSpace(data) != want {
		w.t.Fatalf("%s %s on %s: status %d or message differs from the 7.x contract", method, path, w.cluster.Instances[i].Name, status)
	}
	return data
}

func (w *protocolQualification) pair(from, to int, use string) {
	w.t.Helper()
	var result struct {
		Token string `json:"pairing_token"`
	}
	if json.Unmarshal([]byte(w.admin(from, http.MethodPost, stPairing+use, "", "")), &result) != nil || result.Token == "" {
		w.t.Fatal("pairing did not issue a token")
	}
	parsed, err := pairing.Parse(result.Token)
	if err != nil {
		w.t.Fatal("issued pairing token did not parse")
	}
	parts := strings.Split(result.Token, ".")
	w.secrets = append(w.secrets, parsed.Nonce, parts[2])
	w.admin(to, http.MethodPut, stPairing+use, tokenBody(result.Token),
		`{"message":"Trust with service federated-repo was established successfully"}`)
}

func (w *protocolQualification) pairings(use string, want [][]int) {
	w.t.Helper()
	for i, indexes := range want {
		var entries []struct {
			BaseURL string `json:"base_url"`
		}
		if json.Unmarshal([]byte(w.admin(i, http.MethodGet, stPairings+use, "", "")), &entries) != nil {
			w.t.Fatal("pairings did not return a list")
		}
		got, expected := make([]string, 0, len(entries)), make([]string, 0, len(indexes))
		for _, entry := range entries {
			got = append(got, entry.BaseURL)
		}
		for _, j := range indexes {
			expected = append(expected, w.cluster.Instances[j].URL)
		}
		slices.Sort(got)
		slices.Sort(expected)
		if !slices.Equal(got, expected) {
			w.t.Fatalf("%s lists incorrect %s pairings", w.cluster.Instances[i].Name, use)
		}
	}
}

func (w *protocolQualification) rememberHeaders(t *testing.T, h http.Header) {
	t.Helper()
	sig, err := corepeer.ParseSignature(h)
	if err != nil {
		t.Fatal("wire message carries no usable signature")
	}
	// Signature is an RFC 8941 byte sequence. Keep the encoded value too,
	// since a log could omit the field label while leaking its contents.
	_, value, _ := strings.Cut(h.Get(corepeer.HeaderSignature), "=:")
	w.secrets = append(w.secrets, sig.Params.Nonce, strings.TrimSuffix(value, ":"))
}

// exchange uses a real forward proxy so the production client's own response
// verification sees the altered answer. A local handler standing in for a peer
// would not establish the integrated production-process behavior required here.
func (w *protocolQualification) exchange(t *testing.T, from, to int, corrupt bool) {
	t.Helper()
	id, remote := w.identities[from], w.identities[to]
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	var mu sync.Mutex
	var capturedRequest, capturedResponse http.Header
	var proxyErr error
	calls := 0
	router := chi.NewRouter()
	// The outbound policy CONNECTs even for HTTP, pinning the destination
	// address before opening the tunnel. Unwrap that tunnel here; the route
	// still obtains every answer from the real process through RoundTrip.
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect {
				next.ServeHTTP(rw, r)
				return
			}
			conn, buffer, err := rw.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				return
			}
			if buffer.Flush() != nil {
				return
			}
			inner, err := http.ReadRequest(buffer.Reader)
			if err != nil {
				return
			}
			defer func() { _ = inner.Body.Close() }()
			inner.URL.Scheme, inner.URL.Host = "http", inner.Host
			if "http://"+inner.Host != w.cluster.Instances[to].URL {
				return
			}
			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, inner.WithContext(r.Context()))
			answer := recorder.Result()
			defer func() { _ = answer.Body.Close() }()
			answer.Close = true
			_ = answer.Write(conn)
		})
	})
	router.HandleFunc("/*", func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		forward := r.Clone(r.Context())
		forward.RequestURI = ""
		resp, err := transport.RoundTrip(forward)
		if err != nil {
			proxyErr = err
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			err = corepeer.VerifyResponse(corepeer.Message{
				Status: resp.StatusCode, Header: resp.Header,
				Request: &corepeer.Message{Method: r.Method, TargetURI: r.URL.String(), Header: r.Header},
			},
				body, remote.InstanceID, remote.PublicKey, time.Now())
		}
		if err != nil || resp.StatusCode != http.StatusOK {
			proxyErr = errors.New("actual process did not return a valid signed status answer")
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		capturedRequest, capturedResponse = r.Header.Clone(), resp.Header.Clone()
		for key, values := range resp.Header {
			rw.Header()[key] = values
		}
		if corrupt {
			body = append(body, '\n')
			rw.Header().Del("Content-Length")
		}
		rw.WriteHeader(resp.StatusCode)
		_, _ = rw.Write(body)
	})
	proxy := httptest.NewServer(router)
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := peeradapter.NewFactory(peeradapter.ClientOptions{
		Signer: func(context.Context) (corepeer.Signer, error) {
			return corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey}, nil
		},
		Proxy:                 func(context.Context) (*url.URL, *outbound.NTLMProxy, error) { return proxyURL, nil, nil },
		AllowedPrivateOrigins: []string{w.cluster.Instances[to].URL, proxy.URL}, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := factory.Client(t.Context(), model.Peer{
		InstanceID: remote.InstanceID, PublicKey: remote.PublicKey, BaseURL: w.cluster.Instances[to].URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.cluster.Instances[to].URL+peeradapter.Prefix+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, callErr := client.Do(req)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	mu.Lock()
	defer mu.Unlock()
	if proxyErr != nil || calls != 1 || capturedResponse == nil {
		t.Fatal("observing proxy did not capture exactly one genuine signed process answer")
	}
	w.rememberHeaders(t, capturedRequest)
	w.rememberHeaders(t, capturedResponse)
	if corrupt {
		var unverified *peeradapter.UnverifiedAnswerError
		if resp != nil || !errors.As(callErr, &unverified) || !errors.Is(callErr, corepeer.ErrDigest) {
			t.Fatal("production peer client did not refuse the process answer altered by the proxy")
		}
		return
	}
	if callErr != nil || resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatal("production peer client did not accept the genuine process answer")
	}
	var status struct {
		InstanceID string `json:"instance_id"`
	}
	if json.NewDecoder(resp.Body).Decode(&status) != nil || status.InstanceID != remote.InstanceID {
		t.Fatal("verified status did not identify the destination process")
	}
}

func (w *protocolQualification) refusals(t *testing.T, from, to int) {
	t.Helper()
	id := w.identities[from]
	target := w.cluster.Instances[to].URL + peeradapter.Prefix + "/status"
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	send := func(h http.Header, body string, wantStatus int, wantMessage string, signed bool) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = h.Clone()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("protocol refusal request failed to reach the process")
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal("could not read protocol answer")
		}
		if wantStatus != http.StatusOK {
			w.refused = append(w.refused, string(data))
		}
		var answer struct {
			Message string `json:"message"`
		}
		if resp.StatusCode != wantStatus || wantMessage != "" && (json.Unmarshal(data, &answer) != nil || answer.Message != wantMessage) {
			t.Fatalf("protocol refusal status/message differs: got status %d, want %d", resp.StatusCode, wantStatus)
		}
		if signed {
			remote := w.identities[to]
			if corepeer.VerifyResponse(corepeer.Message{
				Status: resp.StatusCode, Header: resp.Header,
				Request: &corepeer.Message{Method: req.Method, TargetURI: target, Header: h},
			},
				data, remote.InstanceID, remote.PublicKey, time.Now()) != nil {
				t.Fatal("actual process answer did not verify")
			}
			w.rememberHeaders(t, resp.Header)
		} else if resp.Header.Get(corepeer.HeaderSignature) != "" || resp.Header.Get(corepeer.HeaderSignatureInput) != "" {
			t.Fatal("a refusal before authentication unexpectedly carried a signature")
		}
	}
	sign := func(when time.Time, body string) http.Header {
		t.Helper()
		h := http.Header{}
		s := corepeer.Signer{KeyID: id.InstanceID, Key: id.PrivateKey, Now: func() time.Time { return when }}
		if s.SignRequest(http.MethodGet, target, h, []byte(body)) != nil {
			t.Fatal("production signer failed")
		}
		w.rememberHeaders(t, h)
		return h
	}
	h := sign(time.Now(), "")
	send(h, "", http.StatusOK, "", true)
	send(h, "", http.StatusUnauthorized, "The request nonce was already used", false)
	for _, skew := range []time.Duration{-corepeer.Skew - time.Minute, corepeer.Skew + time.Minute} {
		send(sign(time.Now().Add(skew), ""), "", http.StatusUnauthorized,
			"The request time is more than five minutes from this instance's clock", false)
	}
	send(sign(time.Now(), `{}`), `{"tampered":true}`, http.StatusUnauthorized,
		"Content-Digest does not match the request body", false)
	h = sign(time.Now(), "")
	h.Set(corepeer.HeaderDate, time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))
	send(h, "", http.StatusUnauthorized, "The peer signature does not verify", false)
	h = sign(time.Now(), "")
	h.Set(corepeer.HeaderProtocol, "2.0")
	send(h, "", http.StatusBadRequest, "Peer protocol 2.0 is not compatible with this instance's 1.0", true)
	if p, ok := w.cluster.Instances[to].Peer(t, id.InstanceID); !ok || p.ProtocolVersion != 2 {
		t.Fatal("verified version refusal did not record the incompatible peer version")
	}
	send(sign(time.Now(), ""), "", http.StatusOK, "", true)
	if p, ok := w.cluster.Instances[to].Peer(t, id.InstanceID); !ok || p.ProtocolVersion != 1 {
		t.Fatal("compatible signed request did not restore the peer version")
	}
}

func (w *protocolQualification) checkLogs() {
	w.t.Helper()
	leaked := false
	for _, body := range w.refused {
		for _, secret := range w.secrets {
			if secret != "" && strings.Contains(body, secret) {
				leaked = true
			}
		}
	}
	for _, in := range w.cluster.Instances {
		data, err := os.ReadFile(in.LogPath())
		if err != nil {
			w.t.Errorf("could not inspect %s's actual server log", in.Name)
			continue
		}
		clean := data
		for _, secret := range w.secrets {
			if secret != "" && bytes.Contains(clean, []byte(secret)) {
				leaked = true
				clean = bytes.ReplaceAll(clean, []byte(secret), []byte("[REDACTED protocol canary]"))
			}
		}
		// Start prints the server log after a failure. Redact a discovered
		// canary before reporting it, so a regression cannot publish secrets.
		if !bytes.Equal(clean, data) {
			if os.WriteFile(in.LogPath(), clean, 0o600) != nil {
				// Truncating is the fallback if the sanitized write failed.
				_ = os.Truncate(in.LogPath(), 0)
			}
		}
	}
	if leaked {
		w.t.Error("a refusal body or actual server log disclosed a protocol secret canary")
	}
}
