package peers_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccPairingSQLite(t *testing.T) {
	t.Parallel()
	testPairing(t, peers.SQLite)
}

func TestAccPairingPostgres(t *testing.T) {
	t.Parallel()
	testPairing(t, peers.Postgres)
}

const (
	stPairing  = "/artifactory/api/v1/service_trust/pairing/"
	stPairings = "/artifactory/api/v1/service_trust/pairings/"
	stAccess   = "/access/api/v1/service_trust/pairing/"
)

// pairingWorld is a two-instance cluster, A and B, and every answer either
// gave, which must never hold a private key.
type pairingWorld struct {
	t       *testing.T
	a, b    *peers.Instance
	mu      sync.Mutex
	answers []string
}

// call sends method path to in as its administrator, or anonymously when
// anonymous is set, and returns the status and the trimmed body.
func (w *pairingWorld) call(in *peers.Instance, anonymous bool, method, path, body string) (int, string) {
	w.t.Helper()
	user, password := peers.AdminUser, in.AdminPassword
	if anonymous {
		user, password = "", ""
	}
	status, data := in.DoAs(w.t, user, password, method, path, body)
	w.mu.Lock()
	w.answers = append(w.answers, data)
	w.mu.Unlock()
	return status, strings.TrimSpace(data)
}

// expect is call as the administrator that requires status and, when want
// is not empty, body.
func (w *pairingWorld) expect(in *peers.Instance, method, path, body string, status int, want string) string {
	w.t.Helper()
	got, data := w.call(in, false, method, path, body)
	if got != status || want != "" && data != want {
		w.t.Fatalf("%s %s on %s: %d %s, want %d %s", method, path, in.Name, got, data, status, want)
	}
	return data
}

func (w *pairingWorld) token(in *peers.Instance, path string) string {
	w.t.Helper()
	var out struct {
		PairingToken string `json:"pairing_token"`
	}
	if err := json.Unmarshal([]byte(w.expect(in, http.MethodPost, path, "", http.StatusOK, "")), &out); err != nil || out.PairingToken == "" {
		w.t.Fatalf("no pairing token: %v", err)
	}
	return out.PairingToken
}

func (w *pairingWorld) pairings(in *peers.Instance, use string) string {
	w.t.Helper()
	return w.expect(in, http.MethodGet, stPairings+use, "", http.StatusOK, "")
}

func tokenBody(tok string) string { return `{"pairing_token":"` + tok + `"}` }

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
// 7.x's administrators would, and unpairs them (#1926). Each instance is
// reached at its base URL, the address the token and the redeem carry and
// pairing records (#1946); a partition between them fails a redeem and an
// unpair, and healing lets the same token pair them.
func testPairing(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 2, Driver: driver})
	w := &pairingWorld{t: t, a: c.Instances[0], b: c.Instances[1]}
	const established = `{"message":"Trust with service federated-repo was established successfully"}`
	const canceled = `{"message":"Trust with service federated-repo was canceled successfully"}`

	// Refused tokens leave no peer record on either side. A token whose
	// signature was altered, or that names another key, is caught once the
	// issuer has answered; the redeemer then tells it to drop its record.
	tok := w.token(w.a, stPairing+"federated-repo/*")
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/*", tokenBody(tamper(tok)), http.StatusBadRequest,
		`{"message":"Error while token pairing. The pairing token is malformed"}`)
	tok = w.token(w.a, stPairing+"federated-repo/*")
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/*", tokenBody(retoken(t, tok, strings.Repeat("0", 64))), http.StatusBadRequest,
		`{"message":"Error while token pairing. The issuing instance does not hold the key the pairing token names"}`)
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/r", tokenBody(w.token(w.a, stPairing+"federated-repo/*")), http.StatusBadRequest,
		`{"message":"Error while token pairing. The pairing token was issued for use case federated-repo/*"}`)
	w.expect(w.a, http.MethodPut, stPairing+"federated-repo/*", tokenBody(w.token(w.a, stPairing+"federated-repo/*")), http.StatusBadRequest,
		`{"message":"Error while token pairing. Attempt to pair instance with itself"}`)
	for _, in := range []*peers.Instance{w.a, w.b} {
		if got := w.pairings(in, "federated-repo/*"); got != "[]" {
			t.Fatalf("a refused token left pairings %s on %s", got, in.Name)
		}
	}

	// Two concurrent redemptions of one token: exactly one pairs, and each
	// side records the other at its base URL.
	tok = w.token(w.a, stPairing+"federated-repo/*")
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			status, _ := w.call(w.b, false, http.MethodPut, stPairing+"federated-repo/*", tokenBody(tok))
			statuses <- status
		})
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for s := range statuses {
		counts[s]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusBadRequest] != 1 {
		t.Fatalf("concurrent redemptions answered %v", counts)
	}
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo/*", tokenBody(tok), http.StatusBadRequest,
		`{"message":"Error while token pairing. The pairing token was already used"}`)
	if a, b := w.pairings(w.a, "federated-repo/*"), w.pairings(w.b, "federated-repo/*"); a != baseURLs(w.b.URL) || b != baseURLs(w.a.URL) {
		t.Fatalf("pairings after one redemption: A %s, B %s", a, b)
	}

	// Receiving-only trust, the token issued through Access and redeemed
	// through Artifactory, percent-encoded. Across a partition the redeem
	// cannot reach the issuer and pairs nothing; once healed, the same
	// token pairs.
	tok = w.token(w.a, stAccess+"federated-repo%2Fr")
	c.Partition(0, 1)
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo%2Fr", tokenBody(tok), http.StatusBadRequest,
		`{"message":"Error while token pairing. Error exchanging token due to network failure"}`)
	for _, in := range []*peers.Instance{w.a, w.b} {
		if got := w.pairings(in, "federated-repo/r"); got != "[]" {
			t.Fatalf("a redeem across a partition left pairings %s on %s", got, in.Name)
		}
	}
	c.Heal(0, 1)
	w.expect(w.b, http.MethodPut, stPairing+"federated-repo%2Fr", tokenBody(tok), http.StatusOK, established)
	if got := w.pairings(w.b, "federated-repo/r"); got != baseURLs(w.a.URL) {
		t.Fatalf("B's receiving-only pairings %s", got)
	}

	// The answers 7.x gives without a peer.
	w.expect(w.a, http.MethodGet, stPairings+"artifactory-cold", "", http.StatusNotFound, `{"message":"Unknown use case"}`)
	w.expect(w.a, http.MethodGet, stPairings+"distribution", "", http.StatusNotImplemented, "")
	w.expect(w.a, http.MethodDelete, "/artifactory/api/v1/service_trust/revoke/federated-repo/*", "", http.StatusOK,
		`{"message":"Token was successfully revoked"}`)
	w.expect(w.a, http.MethodPost, "/artifactory/api/v1/service_trust/exchange", "{}", http.StatusBadRequest,
		`{"message":"Error while token exchange. Peers of this server sign their requests and hold no token to exchange"}`)
	if status, _ := w.call(w.a, true, http.MethodPost, stPairing+"federated-repo/*", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous issue: %d", status)
	}

	// Unpairing on one side removes the trust on both.
	w.expect(w.b, http.MethodDelete, stPairing+"federated-repo/r", `{"base_url":"`+w.a.URL+`/artifactory"}`, http.StatusOK, canceled)
	if a, b := w.pairings(w.a, "federated-repo/r"), w.pairings(w.b, "federated-repo/r"); a != "[]" || b != "[]" {
		t.Fatalf("pairings after unpairing: A %s, B %s", a, b)
	}
	w.expect(w.a, http.MethodDelete, stPairing+"federated-repo/r", `{"base_url":"`+w.b.URL+`"}`, http.StatusBadRequest,
		`{"message":"Error while cancelling token pairing. Pairing does not exist for instance"}`)

	// A peer that cannot be told, across a partition, keeps the trust here
	// unless forced; the peer that was never told keeps its own.
	c.Partition(0, 1)
	unpairA := `{"base_url":"` + w.a.URL + `"}`
	w.expect(w.b, http.MethodDelete, stPairing+"federated-repo/*", unpairA, http.StatusBadRequest,
		`{"message":"Error while cancelling token pairing. Failed calling revoke token endpoint"}`)
	w.expect(w.b, http.MethodDelete, stPairing+"federated-repo/*?force=true", unpairA, http.StatusOK, canceled)
	c.Heal(0, 1)
	if got := w.pairings(w.b, "federated-repo/*"); got != "[]" {
		t.Fatalf("B's pairings after a forced unpair %s", got)
	}
	if got := w.pairings(w.a, "federated-repo/*"); got != baseURLs(w.b.URL) {
		t.Fatalf("A's pairings after B's forced unpair %s", got)
	}

	// No answer carried either instance's private key.
	for _, in := range []*peers.Instance{w.a, w.b} {
		id := in.Identity(t)
		secrets := []string{
			base64.StdEncoding.EncodeToString(id.PrivateKey), base64.RawURLEncoding.EncodeToString(id.PrivateKey),
			base64.StdEncoding.EncodeToString(id.PrivateKey.Seed()), base64.RawURLEncoding.EncodeToString(id.PrivateKey.Seed()),
		}
		for _, answer := range w.answers {
			for _, s := range secrets {
				if strings.Contains(answer, s) {
					t.Fatalf("an answer carries %s's private key", in.Name)
				}
			}
		}
	}
}
