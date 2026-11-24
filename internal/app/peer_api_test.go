package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/devtools/specoracle"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// peerSpec is the bundle of artifactweave's own peer API (decision 0021).
const peerSpec = "peer.openapi.yaml"

// peerPrefix is the prefix router.go mounts the peer server under; it must
// match the spec's servers[0].url.
const peerPrefix = "/artifactweave/peer/v1"

// TestAccPeerServiceOutsideParityOracle keeps the peer API out of the JFrog
// parity oracle on purpose rather than by omission: its document is marked
// as an extension, which specoracle.LoadOurs leaves out whole; no operation
// names an oracle page; the parity baseline holds nothing under its prefix;
// and no other document carries the marker, so it cannot hide a JFrog
// family. The generated-mux check, the one route check that does not go
// through the oracle, must still cover it.
func TestAccPeerServiceOutsideParityOracle(t *testing.T) {
	doc := loadSpec(t, peerSpec)
	if got := doc.Servers[0].URL; got != peerPrefix {
		t.Fatalf("%s serves under %s, want %s", peerSpec, got, peerPrefix)
	}
	if v, _ := doc.Info.Extensions["x-artifactweave-extension"].(bool); !v {
		t.Errorf("%s info lacks x-artifactweave-extension: true; the parity oracle would compare it", peerSpec)
	}
	ops := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			ops++
			if _, ok := op.Extensions["x-oracle-source"]; ok {
				t.Errorf("%s %s %s has an x-oracle-source; the peer API has no published counterpart",
					peerSpec, method, path)
			}
		}
	}
	if ops == 0 {
		t.Fatalf("%s has no operations", peerSpec)
	}

	root := repopath.Root(t)
	for _, name := range specFiles {
		if name == peerSpec {
			continue
		}
		other := loadSpec(t, name)
		if v, _ := other.Info.Extensions["x-artifactweave-extension"].(bool); v {
			t.Errorf("%s info is marked x-artifactweave-extension; only %s is outside the parity oracle",
				name, peerSpec)
		}
	}
	ours, err := specoracle.LoadOurs(filepath.Join(root, "api", peerSpec))
	if err != nil {
		t.Fatal(err)
	}
	if len(ours.Ops) != 0 {
		t.Errorf("the parity oracle loads %d operations from %s, want none", len(ours.Ops), peerSpec)
	}
	baseline, err := specoracle.ReadBaseline(filepath.Join(root, "api", "parity-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys := slices.Clone(baseline.Missing)
	for k := range baseline.Divergent {
		keys = append(keys, k)
	}
	for _, k := range keys {
		if strings.Contains(k, " "+peerPrefix) {
			t.Errorf("parity-baseline.json lists %s; the peer API has no baseline entry", k)
		}
	}

	if !slices.ContainsFunc(generatedMuxes(), func(g generatedMux) bool { return g.spec == peerSpec }) {
		t.Errorf("%s left the generated-mux check (generatedMuxes)", peerSpec)
	}
}

// TestAccPeerOperationsRefuseUnsignedAtStartUp sends every peer operation,
// with every header it declares but a signature no peer made, to the server
// run starts over SQLite and over PostgreSQL, with anonymous access off as
// it starts. User authentication skips the peer API and the peer transport
// refuses each with 401 in the flat envelope, with user credentials that do
// not verify and without any: no handler runs. TestAccPeerOperationsSigned
// BetweenInstances sends them signed.
func TestAccPeerOperationsRefuseUnsignedAtStartUp(t *testing.T) {
	doc := loadSpec(t, peerSpec)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Listen = "127.0.0.1:0"
			if backend == "postgres" {
				// Before run's cleanup, so the schema outlives the server.
				cfg.Database.Driver = "postgres"
				cfg.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
			}
			ctx, cancel := context.WithCancel(context.Background())
			ready := make(chan string, 1)
			done := make(chan error, 1)
			go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), ready) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			var addr string
			select {
			case addr = <-ready:
			case err := <-done:
				t.Fatalf("run: %v", err)
			case <-time.After(2 * time.Minute):
				t.Fatal("the server did not start")
			}
			client := &http.Client{Timeout: 30 * time.Second}
			checked := 0
			for path, item := range doc.Paths.Map() {
				for method, op := range item.Operations() {
					checked++
					for _, authorization := range []string{"", "Basic YWRtaW46d3Jvbmc="} {
						status, body := sendPeerOperation(
							t, client, "http://"+addr, method, path, op, authorization,
						)
						want, code := http.StatusUnauthorized, "UNAUTHORIZED"
						if method == http.MethodPost && path == "/pair" {
							// The redeem is verified with the key its body
							// presents, and "{}" presents none.
							want, code = http.StatusBadRequest, "BAD_REQUEST"
						}
						var got map[string]any
						if status != want || json.Unmarshal(body, &got) != nil ||
							got["code"] != code || len(got) != 2 {
							t.Errorf("%s %s (Authorization %q): status %d, body %s, want the flat %d",
								method, path, authorization, status, body, want)
						}
					}
				}
			}
			if checked != 8 {
				t.Errorf("checked %d peer operations, want the 8 the spec declares", checked)
			}
		})
	}
}

// sendPeerOperation sends one operation of the peer spec with a value for
// every required parameter, and authorization when it is not empty, and
// returns the status and body.
func sendPeerOperation(
	t *testing.T,
	client *http.Client,
	base, method, path string,
	op *openapi3.Operation,
	authorization string,
) (int, []byte) {
	t.Helper()
	target := peerPrefix + fillParams(path)
	var query []string
	for _, p := range op.Parameters {
		if p.Value.Required && p.Value.In == openapi3.ParameterInQuery {
			query = append(query, p.Value.Name+"="+plausibleValue(p.Value))
		}
	}
	if len(query) > 0 {
		target += "?" + strings.Join(query, "&")
	}
	body, contentType := plausibleBody(op.RequestBody)
	req, err := http.NewRequestWithContext(t.Context(), method, base+target, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	for _, p := range op.Parameters {
		if p.Value.Required && p.Value.In == openapi3.ParameterInHeader {
			req.Header.Set(p.Value.Name, plausibleValue(p.Value))
		}
	}
	// Well-formed signed headers that no paired peer made.
	req.Header.Set("Artifactweave-Peer-Protocol", "1.0")
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("Signature-Input",
		`peer=("@method" "@target-uri" "date");created=1;keyid="jfrt@x";alg="ed25519";nonce="n"`)
	req.Header.Set("Signature", "peer=:AAAA:")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// peerSite is one running instance for the peer transport scenarios: its
// runtime, a listener over the router serve builds, and what it logs.
type peerSite struct {
	rt     *Runtime
	server *httptest.Server
	log    *syncBuffer
	self   model.Peer
	key    ed25519.PrivateKey
}

func openPeerSite(t *testing.T, backend string) *peerSite {
	t.Helper()
	cfg := testConfig(t)
	if backend == "postgres" {
		cfg.Database.Driver = "postgres"
		cfg.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
	}
	site := &peerSite{log: &syncBuffer{}}
	logger := slog.New(slog.NewTextHandler(site.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt, err := OpenRuntime(t.Context(), cfg, logger, RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	site.rt = rt
	site.server = httptest.NewServer(NewRouter(RouterConfig{
		Store: rt.Store, Blobs: rt.Blobs, Auth: rt.Auth, Tokens: rt.Tokens,
		Access: rt.modules.access.API(), Configuration: rt.modules.configuration.API(),
		Logging: rt.Logging,
	}))
	t.Cleanup(func() {
		site.server.Close()
		if err := rt.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	id, err := rt.Store.Instance().Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	site.key = id.PrivateKey
	site.self = model.Peer{
		InstanceID: id.InstanceID, BaseURL: site.server.URL, PublicKey: id.PublicKey,
		ProtocolVersion: instance.ProtocolVersion,
	}
	return site
}

// pair records each site as the other's peer, as PEER-02's pairing will.
func pair(t *testing.T, a, b *peerSite) {
	t.Helper()
	if err := a.rt.Store.Peers().Put(t.Context(), b.self); err != nil {
		t.Fatal(err)
	}
	if err := b.rt.Store.Peers().Put(t.Context(), a.self); err != nil {
		t.Fatal(err)
	}
}

// peerBody is a body each peer operation accepts: POST /pair presents the
// caller's identity, which its signature must verify with.
func peerBody(from *peerSite, method, path string, op *openapi3.Operation) ([]byte, string) {
	if method == http.MethodPost && path == "/pair" {
		b, _ := json.Marshal(map[string]string{
			"nonce": "n", "instance_id": from.self.InstanceID, "base_url": from.self.BaseURL,
			"public_key": base64.StdEncoding.EncodeToString(from.self.PublicKey), "protocol_version": "1.0",
		})
		return b, "application/json"
	}
	r, contentType := plausibleBody(op.RequestBody)
	b, _ := io.ReadAll(r)
	return b, contentType
}

// TestAccPeerOperationsSignedBetweenInstances runs two instances over
// SQLite and over PostgreSQL, pairs them through their peer stores, and
// sends every peer operation from B to A through B's peer client: A
// verifies each request and answers its 501 signed, and B verifies each
// answer. Then a replayed, a tampered and a misdirected request, and one of
// another major protocol version, are refused live; the mismatch is
// recorded on A's record of B. Neither instance logs a signature, nonce or
// key.
func TestAccPeerOperationsSignedBetweenInstances(t *testing.T) {
	doc := loadSpec(t, peerSpec)
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			a, b := openPeerSite(t, backend), openPeerSite(t, backend)
			pair(t, a, b)
			clients, err := newPeerClients(b.rt.Store, []string{a.server.URL}, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			client, err := clients.Client(t.Context(), a.self)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var secrets []string
			checked := 0
			for path, item := range doc.Paths.Map() {
				for method, op := range item.Operations() {
					checked++
					body, contentType := peerBody(b, method, path, op)
					target := a.server.URL + peerPrefix + fillParams(path)
					var query []string
					for _, p := range op.Parameters {
						if p.Value.Required && p.Value.In == openapi3.ParameterInQuery {
							query = append(query, p.Value.Name+"="+url.QueryEscape(plausibleValue(p.Value)))
						}
					}
					if len(query) > 0 {
						target += "?" + strings.Join(query, "&")
					}
					req, err := http.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					if contentType != "" {
						req.Header.Set("Content-Type", contentType)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Errorf("%s %s: %v", method, path, err)
						continue
					}
					got, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode != http.StatusNotImplemented || !strings.Contains(string(got), `"NOT_IMPLEMENTED"`) {
						t.Errorf("%s %s: status %d, body %s, want the signed 501", method, path, resp.StatusCode, got)
					}
					secrets = append(secrets, req.Header.Get("Signature"), resp.Header.Get("Signature"))
				}
			}
			if checked != 8 {
				t.Errorf("checked %d peer operations, want 8", checked)
			}
			secrets = append(secrets, refusedLive(t, a, b)...)

			for _, site := range []*peerSite{a, b} {
				secrets = append(secrets,
					base64.StdEncoding.EncodeToString(site.key),
					base64.StdEncoding.EncodeToString(site.key.Seed()),
					base64.StdEncoding.EncodeToString(site.self.PublicKey))
			}
			logs := a.log.String() + b.log.String()
			if !strings.Contains(logs, peerPrefix) {
				t.Fatal("the instances logged no peer request; the log check would prove nothing")
			}
			for _, secret := range secrets {
				secret = strings.TrimSuffix(strings.TrimPrefix(secret, "peer=:"), ":")
				if secret != "" && strings.Contains(logs, secret) {
					t.Errorf("a signature, nonce or key appears in the logs")
				}
			}
		})
	}
}

// refusedLive sends B's signed requests to A by hand, replayed, tampered,
// misdirected and of another major version, checks each refusal, and
// returns the signatures and nonces it used.
func refusedLive(t *testing.T, a, b *peerSite) []string {
	t.Helper()
	signer := corepeer.Signer{KeyID: b.self.InstanceID, Key: b.key}
	send := func(r *http.Request) (int, string) {
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	signed := func(target string, body []byte) *http.Request {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := signer.SignRequest(r.Method, target, r.Header, body); err != nil {
			t.Fatal(err)
		}
		return r
	}
	var secrets []string
	remember := func(r *http.Request) {
		s, _ := corepeer.ParseSignature(r.Header)
		secrets = append(secrets, r.Header.Get("Signature"), s.Params.Nonce)
	}
	events := a.server.URL + peerPrefix + "/streams/repo:libs/events"
	body := []byte(`{"origin":"x","from_seq":1,"events":[]}`)

	r := signed(events, body)
	remember(r)
	replay := r.Clone(t.Context())
	replay.Body = io.NopCloser(bytes.NewReader(body))
	if status, got := send(r); status != http.StatusNotImplemented {
		t.Errorf("the original: %d %s", status, got)
	}
	if status, got := send(replay); status != http.StatusUnauthorized || !strings.Contains(got, "already used") {
		t.Errorf("a replay: %d %s", status, got)
	}

	r = signed(events, body)
	remember(r)
	r.Body = io.NopCloser(strings.NewReader(`{"origin":"y","from_seq":1,"events":[]}`))
	if status, got := send(r); status != http.StatusUnauthorized || !strings.Contains(got, "Content-Digest") {
		t.Errorf("a tampered body: %d %s", status, got)
	}

	// Signed for B's own listener and sent to A: the target URI differs.
	r = signed(b.server.URL+peerPrefix+"/streams/repo:libs/events", body)
	remember(r)
	r.URL, _ = url.Parse(events)
	r.Host = r.URL.Host
	if status, got := send(r); status != http.StatusUnauthorized || !strings.Contains(got, "does not verify") {
		t.Errorf("a request signed for another instance: %d %s", status, got)
	}

	r = signed(events, body)
	remember(r)
	r.Header.Set(corepeer.HeaderProtocol, "2.0")
	if status, got := send(r); status != http.StatusBadRequest || !strings.Contains(got, "not compatible") {
		t.Errorf("another major version: %d %s", status, got)
	}
	recorded, err := a.rt.Store.Peers().Get(t.Context(), b.self.InstanceID)
	if err != nil || recorded.ProtocolVersion != 2 ||
		recorded.DeriveStatus(time.Now(), time.Hour, false, 0) != model.PeerOffline {
		t.Errorf("A's record of B after the mismatch: %+v, %v", recorded, err)
	}
	r = signed(events, body)
	remember(r)
	if status, got := send(r); status != http.StatusNotImplemented {
		t.Errorf("a compatible request after the mismatch: %d %s", status, got)
	}
	if recorded, _ = a.rt.Store.Peers().Get(t.Context(), b.self.InstanceID); !recorded.Compatible() {
		t.Errorf("A's record of B after a compatible request: %+v", recorded)
	}
	return secrets
}

// TestUnitPeerGuardWithoutStore: a router with no store has no peers, so
// it refuses a peer request as one with an empty peer table would, and
// passes every other request through.
func TestUnitPeerGuardWithoutStore(t *testing.T) {
	served := 0
	h := peerGuard(nil, time.Now)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served++ }))
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signer := corepeer.Signer{KeyID: "jfrt@x", Key: key}
	r := httptest.NewRequest(http.MethodGet, peerPrefix+"/status", nil)
	if err := signer.SignRequest(r.Method, "http://"+r.Host+peerPrefix+"/status", r.Header, nil); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || served != 0 ||
		!strings.Contains(rec.Body.String(), "no paired peer") {
		t.Errorf("a signed peer request without a store: %d %s, served %d", rec.Code, rec.Body, served)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/artifactweave/nothing", nil))
	if served != 1 {
		t.Error("a request outside the peer prefix was not passed through")
	}
	var none noPeers
	if err := none.SetProtocolVersion(t.Context(), "jfrt@x", 2); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("SetProtocolVersion: %v", err)
	}
	if err := none.Remember(t.Context(), []byte{1}, time.Now(), time.Now()); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("Remember: %v", err)
	}
	if n, err := none.Prune(t.Context(), time.Now()); n != 0 || err != nil {
		t.Errorf("Prune: %d, %v", n, err)
	}
	if _, err := newPeerClients(nil, []string{"::"}, time.Now); err == nil {
		t.Error("peer clients with a malformed private origin")
	}
}
