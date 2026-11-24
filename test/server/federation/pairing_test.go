package federation_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// pairingInstance is one running instance: its runtime and its listener.
type pairingInstance struct {
	runtime *app.Runtime
	server  *httptest.Server
	url     string
}

// pairingWorld is two instances that may call each other, A and B, and
// every answer either gave, which must never hold a private key.
type pairingWorld struct {
	t       *testing.T
	a, b    *pairingInstance
	client  *http.Client
	mu      sync.Mutex
	answers []string
}

// newPairingWorld opens two runtimes over dsnA and dsnB, each allowed to
// call the other's loopback listener, which is bound before the runtimes
// open so each knows the other's origin.
func newPairingWorld(t *testing.T, driver, dsnA, dsnB string) *pairingWorld {
	t.Helper()
	listeners := make([]net.Listener, 2)
	origins := make([]string, 2)
	for i := range listeners {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i], origins[i] = l, "http://"+l.Addr().String()
	}
	open := func(l net.Listener, dsn string) *pairingInstance {
		cfg := pushConfig(t, driver, dsn)
		cfg.Remote.AllowedPrivateOrigins = origins
		runtime, err := app.OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), harness.AppOptions())
		if err != nil {
			t.Fatal("open runtime", err)
		}
		server := httptest.NewUnstartedServer(harness.Router(harness.Config{
			Store: runtime.Store, Blobs: runtime.Blobs, Auth: runtime.Auth, Tokens: runtime.Tokens, Keys: runtime.Keys,
			Federation: runtime.Federation, PeerRemote: runtime.PeerRemote, Work: runtime.Work,
			PasswordHash: support.TestHash, Logger: slog.New(slog.DiscardHandler),
		}))
		_ = server.Listener.Close()
		server.Listener = l
		server.Start()
		t.Cleanup(func() {
			server.Close()
			if err := runtime.Close(context.Background()); err != nil {
				t.Error("close runtime", err)
			}
		})
		return &pairingInstance{runtime: runtime, server: server, url: server.URL}
	}
	w := &pairingWorld{t: t, client: &http.Client{Timeout: 30 * time.Second}}
	w.a, w.b = open(listeners[0], dsnA), open(listeners[1], dsnB)
	t.Cleanup(w.client.CloseIdleConnections)
	return w
}

// call sends method path to in as admin (or as who, "user:password", or
// anonymously when who is "-") and returns the status and body.
func (w *pairingWorld) call(in *pairingInstance, who, method, path, body string) (int, string) {
	w.t.Helper()
	req, err := http.NewRequestWithContext(w.t.Context(), method, in.url+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	if who == "" {
		who = "admin:pw"
	}
	if who != "-" {
		name, password, _ := strings.Cut(who, ":")
		req.SetBasicAuth(name, password)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	w.mu.Lock()
	w.answers = append(w.answers, string(data))
	w.mu.Unlock()
	return resp.StatusCode, strings.TrimSpace(string(data))
}

// expect is call that requires status and, when want is not empty, body.
func (w *pairingWorld) expect(in *pairingInstance, method, path, body string, status int, want string) string {
	w.t.Helper()
	got, data := w.call(in, "", method, path, body)
	if got != status || want != "" && data != want {
		w.t.Fatalf("%s %s: %d %s, want %d %s", method, path, got, data, status, want)
	}
	return data
}

const (
	stPairing  = "/artifactory/api/v1/service_trust/pairing/"
	stPairings = "/artifactory/api/v1/service_trust/pairings/"
	stAccess   = "/access/api/v1/service_trust/pairing/"
)

func (w *pairingWorld) token(in *pairingInstance, path string) string {
	w.t.Helper()
	var out struct {
		PairingToken string `json:"pairing_token"`
	}
	if err := json.Unmarshal([]byte(w.expect(in, http.MethodPost, path, "", 200, "")), &out); err != nil || out.PairingToken == "" {
		w.t.Fatalf("no pairing token: %v", err)
	}
	return out.PairingToken
}

func tokenBody(tok string) string { return `{"pairing_token":"` + tok + `"}` }

func (w *pairingWorld) pairings(in *pairingInstance, use string) string {
	w.t.Helper()
	return w.expect(in, http.MethodGet, stPairings+use, "", 200, "")
}

func baseURLs(urls ...string) string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		out = append(out, `{"base_url":"`+u+`"}`)
	}
	return "[" + strings.Join(out, ",") + "]"
}

// tamper changes the first character of tok's signature, which still
// decodes to a signature of the right size.
func tamper(tok string) string {
	i := strings.LastIndex(tok, ".") + 1
	c := byte('A')
	if tok[i] == 'A' {
		c = 'B'
	}
	return tok[:i] + string(c) + tok[i+1:]
}

// retoken re-encodes tok's payload with key_sha256 replaced, keeping the
// issuer's signature over the original.
func retoken(t *testing.T, tok, fingerprint string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	claims["key_sha256"] = fingerprint
	payload, err = json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	return strings.Join(parts, ".")
}

// testPairing pairs two instances through the service_trust endpoints, as
// 7.x's administrators would, and unpairs them (#1926). It runs two
// runtimes here rather than in test/peers: that harness lets an instance
// reach another only through a link, while pairing records the base URL a
// token or a redeem carries, the instance's own.
func testPairing(t *testing.T, driver, dsnA, dsnB string) {
	w := newPairingWorld(t, driver, dsnA, dsnB)
	const established = `{"message":"Trust with service federated-repo was established successfully"}`
	const canceled = `{"message":"Trust with service federated-repo was canceled successfully"}`

	// Refused tokens leave no peer record on either side. A token whose
	// signature was altered, or that names another key, is caught once the
	// issuer has answered; the redeemer then tells it to drop its record.
	tok := w.token(w.a, stPairing+"federated-repo/*")
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/*", tokenBody(tamper(tok)), 400,
		`{"message":"Error while token pairing. The pairing token is malformed"}`)
	tok = w.token(w.a, stPairing+"federated-repo/*")
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/*", tokenBody(retoken(t, tok, strings.Repeat("0", 64))), 400,
		`{"message":"Error while token pairing. The issuing instance does not hold the key the pairing token names"}`)
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/r", tokenBody(w.token(w.a, stPairing+"federated-repo/*")), 400,
		`{"message":"Error while token pairing. The pairing token was issued for use case federated-repo/*"}`)
	w.expect(w.a, http.MethodPut, stPairing+"federated-repo/*", tokenBody(w.token(w.a, stPairing+"federated-repo/*")), 400,
		`{"message":"Error while token pairing. Attempt to pair instance with itself"}`)
	for _, in := range []*pairingInstance{w.a, w.b} {
		if got := w.pairings(in, "federated-repo/*"); got != "[]" {
			t.Fatalf("a refused token left pairings %s", got)
		}
	}

	// Two concurrent redemptions of one token: exactly one pairs.
	tok = w.token(w.a, stPairing+"federated-repo/*")
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			status, _ := w.call(w.b, "", http.MethodPut, stPairing+"federated-repo/*", tokenBody(tok))
			statuses <- status
		})
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for s := range statuses {
		counts[s]++
	}
	if counts[200] != 1 || counts[400] != 1 {
		t.Fatalf("concurrent redemptions answered %v", counts)
	}
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/*", tokenBody(tok), 400,
		`{"message":"Error while token pairing. The pairing token was already used"}`)
	if a, b := w.pairings(w.a, "federated-repo/*"), w.pairings(w.b, "federated-repo/*"); a != baseURLs(w.b.url) || b != baseURLs(w.a.url) {
		t.Fatalf("pairings after one redemption: A %s, B %s", a, b)
	}

	// Receiving-only trust, the token issued through Access and redeemed
	// through Artifactory, percent-encoded.
	tok = w.token(w.a, stAccess+"federated-repo%2Fr")
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo%2Fr", tokenBody(tok), 200, established)
	if got := w.pairings(w.b, "federated-repo/r"); got != baseURLs(w.a.url) {
		t.Fatalf("B's receiving-only pairings %s", got)
	}

	// The answers 7.x gives without a peer.
	w.expect(w.a, http.MethodGet, stPairings+"artifactory-cold", "", 404, `{"message":"Unknown use case"}`)
	w.expect(w.a, http.MethodGet, stPairings+"distribution", "", 501, "")
	w.expect(w.a, http.MethodDelete, "/artifactory/api/v1/service_trust/revoke/federated-repo/*", "", 200,
		`{"message":"Token was successfully revoked"}`)
	w.expect(w.a, http.MethodPost, "/artifactory/api/v1/service_trust/exchange", "{}", 400,
		`{"message":"Error while token exchange. Peers of this server sign their requests and hold no token to exchange"}`)
	if status, _ := w.call(w.a, "-", http.MethodPost, stPairing+"federated-repo/*", ""); status != 401 {
		t.Fatalf("anonymous issue: %d", status)
	}

	// Unpairing on one side removes the trust on both.
	w.expect(w.b, http.MethodDelete, stPairing+"federated-repo/r", `{"base_url":"`+w.a.url+`/artifactory"}`, 200, canceled)
	if a, b := w.pairings(w.a, "federated-repo/r"), w.pairings(w.b, "federated-repo/r"); a != "[]" || b != "[]" {
		t.Fatalf("pairings after unpairing: A %s, B %s", a, b)
	}
	w.expect(w.a, http.MethodDelete, stPairing+"federated-repo/r", `{"base_url":"`+w.b.url+`"}`, 400,
		`{"message":"Error while cancelling token pairing. Pairing does not exist for instance"}`)

	// A peer that cannot be told keeps the trust here unless forced.
	w.a.server.Close()
	unpairA := `{"base_url":"` + w.a.url + `"}`
	w.expect(w.b, http.MethodDelete, stPairing+"federated-repo/*", unpairA, 400,
		`{"message":"Error while cancelling token pairing. Failed calling revoke token endpoint"}`)
	w.expect(w.b, http.MethodDelete, stPairing+"federated-repo/*?force=true", unpairA, 200, canceled)
	if got := w.pairings(w.b, "federated-repo/*"); got != "[]" {
		t.Fatalf("B's pairings after a forced unpair %s", got)
	}

	// No answer carried either instance's private key.
	for _, in := range []*pairingInstance{w.a, w.b} {
		id, err := in.runtime.Store.Instance().Get(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		secrets := []string{
			base64.StdEncoding.EncodeToString(id.PrivateKey), base64.RawURLEncoding.EncodeToString(id.PrivateKey),
			base64.StdEncoding.EncodeToString(id.PrivateKey.Seed()), base64.RawURLEncoding.EncodeToString(id.PrivateKey.Seed()),
		}
		for _, answer := range w.answers {
			for _, s := range secrets {
				if strings.Contains(answer, s) {
					t.Fatal("an answer carries an instance's private key")
				}
			}
		}
	}
}

func TestAccPairingSQLite(t *testing.T) {
	t.Parallel()
	testPairing(t, "", "", "")
}

func TestAccPairingPostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testPairing(t, "postgres", sqltest.ScratchDSN(t, dsn), sqltest.ScratchDSN(t, dsn))
}
