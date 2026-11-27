package peers_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccPeerProtocolSQLite(t *testing.T)   { t.Parallel(); testPeerProtocol(t, peers.SQLite) }
func TestAccPeerProtocolPostgres(t *testing.T) { t.Parallel(); testPeerProtocol(t, peers.Postgres) }

func testPeerProtocol(t *testing.T, driver peers.Driver) {
	c, all := federation(t, driver)
	pairReceiver(t, all[0], all[2])
	for _, in := range all {
		_, body := in.Do(t, http.MethodGet, stPairings+"federated-repo/*", "")
		for _, other := range all {
			if in != other && !strings.Contains(body, other.URL) {
				t.Fatal("pairings omitted a member")
			}
		}
	}
	var sensitive []string
	var privateKeys [][]byte
	client := &http.Client{Timeout: 10 * time.Second}
	for i, from := range all {
		identity := from.Identity(t)
		privateKeys = append(privateKeys, identity.PrivateKey)
		sensitive = append(sensitive, base64.StdEncoding.EncodeToString(identity.PrivateKey))
		to := all[(i+1)%len(all)]
		remote := to.Identity(t)
		newRequest := func(when time.Time) *http.Request {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, to.URL+"/artifactweave/peer/v1/status", nil)
			if err != nil {
				t.Fatal(err)
			}
			signer := corepeer.Signer{KeyID: identity.InstanceID, Key: identity.PrivateKey, Now: func() time.Time { return when }}
			if err := signer.SignRequest(req.Method, req.URL.String(), req.Header, nil); err != nil {
				t.Fatal(err)
			}
			sensitive = append(sensitive, req.Header.Get(corepeer.HeaderSignature), req.Header.Get(corepeer.HeaderSignatureInput))
			return req
		}
		call := func(req *http.Request, want int, signed bool) {
			t.Helper()
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal("live peer request failed:", err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != want {
				t.Fatalf("peer response status %d, want %d", resp.StatusCode, want)
			}
			for _, in := range all {
				if bytes.Contains(body, in.Identity(t).PrivateKey) {
					t.Fatal("response disclosed a private key")
				}
			}
			if signed {
				msg := corepeer.Message{Method: req.Method, TargetURI: req.URL.String(), Header: req.Header}
				answer := corepeer.Message{Status: resp.StatusCode, Header: resp.Header, Request: &msg}
				if err := corepeer.VerifyResponse(answer, body, remote.InstanceID, remote.PublicKey, time.Now()); err != nil {
					t.Fatal("live response signature failed:", err)
				}
				if err := corepeer.VerifyResponse(answer, append(bytes.Clone(body), 'x'), remote.InstanceID, remote.PublicKey, time.Now()); err == nil {
					t.Fatal("tampered response verified")
				}
				sensitive = append(sensitive, resp.Header.Get(corepeer.HeaderSignature), resp.Header.Get(corepeer.HeaderSignatureInput))
			} else if resp.Header.Get(corepeer.HeaderSignature) != "" || resp.Header.Get(corepeer.HeaderSignatureInput) != "" {
				t.Fatal("preauthentication refusal was signed")
			}
			if want != http.StatusOK {
				for _, secret := range sensitive {
					if secret != "" && bytes.Contains(body, []byte(secret)) {
						t.Fatal("refusal disclosed protocol material")
					}
				}
			}
		}
		req := newRequest(time.Now())
		call(req, http.StatusOK, true)
		call(req.Clone(t.Context()), http.StatusUnauthorized, false)
		call(newRequest(time.Now().Add(-10*time.Minute)), http.StatusUnauthorized, false)
		tampered := newRequest(time.Now())
		tampered.URL.RawQuery = "tampered=true"
		call(tampered, http.StatusUnauthorized, false)
		version := newRequest(time.Now())
		version.Header.Set(corepeer.HeaderProtocol, "99.0")
		call(version, http.StatusBadRequest, true)
		call(newRequest(time.Now()), http.StatusOK, true)
	}
	// Cancelling one edge removes only that trust, on both sides.
	body := `{"base_url":"` + all[1].URL + `"}`
	status, answer := all[0].Do(t, http.MethodDelete, stPairing+"federated-repo/*", body)
	if status != http.StatusOK || strings.TrimSpace(answer) != `{"message":"Trust with service federated-repo was canceled successfully"}` {
		t.Fatalf("cancel trust status %d or message differs", status)
	}
	for _, side := range [][2]int{{0, 1}, {1, 0}} {
		status, body := all[side[0]].Do(t, http.MethodGet, stPairings+"federated-repo/*", "")
		var got []struct {
			BaseURL string `json:"base_url"`
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
			t.Fatal("invalid pairings after cancellation")
		}
		for _, p := range got {
			if p.BaseURL == all[side[1]].URL {
				t.Fatal("cancelled trust remains")
			}
		}
	}
	c.Close()
	for _, in := range all {
		data, err := os.ReadFile(in.LogPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range privateKeys {
			if bytes.Contains(data, key) {
				t.Fatal("server log disclosed a private key")
			}
		}
		for _, secret := range sensitive {
			if secret != "" && bytes.Contains(data, []byte(secret)) {
				t.Fatal("server log disclosed protocol material")
			}
		}
	}
}
