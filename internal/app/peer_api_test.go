package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/devtools/specoracle"
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

// TestAccPeerOperationsAnswer501AtStartUp sends every peer operation, signed
// headers and all, to the server run starts over SQLite and over PostgreSQL,
// with anonymous access off as it starts. User authentication skips the peer
// API, so each answers 501 in the flat envelope until its P13 ticket serves
// it, both without user credentials and with ones that do not verify.
func TestAccPeerOperationsAnswer501AtStartUp(t *testing.T) {
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
						if status != http.StatusNotImplemented {
							t.Errorf("%s %s (Authorization %q): status %d, body %s",
								method, path, authorization, status, body)
							continue
						}
						var got map[string]any
						if err := json.Unmarshal(body, &got); err != nil ||
							got["code"] != "NOT_IMPLEMENTED" ||
							got["message"] != "not implemented by artifactweave yet" || len(got) != 2 {
							t.Errorf("%s %s: body %s, want the flat 501 envelope", method, path, body)
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
