package peers_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccPeerProtocolSQLite(t *testing.T)   { t.Parallel(); testPeerProtocol(t, peers.SQLite) }
func TestAccPeerProtocolPostgres(t *testing.T) { t.Parallel(); testPeerProtocol(t, peers.Postgres) }

func testPeerProtocol(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver})
	all := c.Instances
	var keys, sensitive []string
	for _, in := range all {
		key := in.Identity(t).PrivateKey
		for _, material := range [][]byte{key, key.Seed()} {
			keys = append(keys, string(material), hex.EncodeToString(material))
			for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
				keys = append(keys, encoding.EncodeToString(material))
			}
		}
	}
	pairForProof := func(issuer, redeemer *peers.Instance, use string) {
		status, body := issuer.Do(t, http.MethodPost, stPairing+use, "")
		assertProtocolRedacted(t, []byte(body), keys)
		var token struct {
			Value string `json:"pairing_token"`
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &token) != nil || token.Value == "" {
			t.Fatal("pairing token was not issued")
		}
		sensitive = append(sensitive, token.Value)
		parts := strings.Split(token.Value, ".")
		if len(parts) != 3 {
			t.Fatal("unexpected pairing token shape")
		}
		sensitive = append(sensitive, parts[2])
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal("invalid pairing token payload")
		}
		var claims struct {
			Nonce string `json:"nonce"`
		}
		if json.Unmarshal(payload, &claims) != nil || claims.Nonce == "" {
			t.Fatal("pairing token omitted nonce")
		}
		sensitive = append(sensitive, claims.Nonce)
		status, body = redeemer.Do(t, http.MethodPut, stPairing+use, tokenBody(token.Value))
		assertProtocolRedacted(t, []byte(body), append(slices.Clone(keys), sensitive...))
		if status != http.StatusOK || strings.TrimSpace(body) != `{"message":"Trust with service federated-repo was established successfully"}` {
			t.Fatal("pairing redemption failed")
		}
	}
	for i, in := range all {
		for _, other := range all[i+1:] {
			pairForProof(in, other, "federated-repo/*")
		}
	}
	pairForProof(all[0], all[2], "federated-repo/r")
	for _, in := range all {
		_, body := in.Do(t, http.MethodGet, stPairings+"federated-repo/*", "")
		assertProtocolRedacted(t, []byte(body), append(slices.Clone(keys), sensitive...))
		for _, other := range all {
			if in != other && !strings.Contains(body, other.URL) {
				t.Fatal("pairings omitted a member")
			}
		}
	}
	captureSignature := func(header http.Header) {
		parsed, err := corepeer.ParseSignature(header)
		if err != nil {
			t.Fatal("invalid signature fields")
		}
		sensitive = append(sensitive, header.Get(corepeer.HeaderSignature), header.Get(corepeer.HeaderSignatureInput), parsed.Params.Nonce)
		_, value, _ := strings.Cut(header.Get(corepeer.HeaderSignature), "=")
		sensitive = append(sensitive, strings.Trim(value, ":"))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for i, from := range all {
		identity := from.Identity(t)
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
			captureSignature(req.Header)
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
			assertProtocolRedacted(t, body, append(slices.Clone(keys), sensitive...))
			if signed {
				msg := corepeer.Message{Method: req.Method, TargetURI: req.URL.String(), Header: req.Header}
				answer := corepeer.Message{Status: resp.StatusCode, Header: resp.Header, Request: &msg}
				if err := corepeer.VerifyResponse(answer, body, remote.InstanceID, remote.PublicKey, time.Now()); err != nil {
					t.Fatal("live response signature failed:", err)
				}
				if err := corepeer.VerifyResponse(answer, append(bytes.Clone(body), 'x'), remote.InstanceID, remote.PublicKey, time.Now()); err == nil {
					t.Fatal("tampered response verified")
				}
				captureSignature(resp.Header)
			} else if resp.Header.Get(corepeer.HeaderSignature) != "" || resp.Header.Get(corepeer.HeaderSignatureInput) != "" {
				t.Fatal("preauthentication refusal was signed")
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
		assertProtocolRedacted(t, data, append(slices.Clone(keys), sensitive...))
	}
}

// No assertion includes the matching bytes: failing evidence must stay redacted.
func assertProtocolRedacted(t *testing.T, data []byte, forbidden []string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && bytes.Contains(data, []byte(value)) {
			t.Fatal("response or server log disclosed sensitive protocol material")
		}
	}
}
