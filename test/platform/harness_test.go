// Package platform checks a reference deployment end to end: artifactweave
// started against a scenario that platform-scenarios.yml has just applied
// (decision 0030). The suite runs only when ARTIFACTWEAVE_SCENARIO names the
// scenario, so `go test ./...` passes over it; scenario runs are evidence
// that a deployment works, not coverage.
//
// The workflow sets:
//
//	ARTIFACTWEAVE_SCENARIO               <cloud>/<scenario>, for example gcp/single-bucket
//	ARTIFACTWEAVE_SCENARIO_CONFIG        the scenario's artifactweave_config output (YAML)
//	ARTIFACTWEAVE_SCENARIO_STORAGE_HOST  the scenario's storage_host output
//	ARTIFACTWEAVE_BIN                    the artifactweave binary built in the job
package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// check is one end-to-end property of a deployment.
type check struct {
	name string
	run  func(t *testing.T, h *harness)
}

// scenarioChecks holds the checks a scenario adds to the generic ones, keyed
// by ARTIFACTWEAVE_SCENARIO. Later scenarios register theirs from an init
// function in their own file: mirror copies through storageinfo (#130),
// the CDN host and edge refusals (#132 to #134).
var scenarioChecks = map[string][]check{}

var errNotTCP = errors.New("listener address is not TCP")

func TestMain(m *testing.M) {
	if os.Getenv("ARTIFACTWEAVE_SCENARIO") == "" {
		fmt.Println(
			"skipping test/platform: ARTIFACTWEAVE_SCENARIO is not set (platform-scenarios.yml sets it)",
		)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestScenario starts artifactweave against the scenario, then runs the
// generic checks followed by the scenario's own.
func TestScenario(t *testing.T) {
	scenario := os.Getenv("ARTIFACTWEAVE_SCENARIO")
	h := start(t)
	checks := append(genericChecks(), scenarioChecks[scenario]...)
	for _, c := range checks {
		if !t.Run(c.name, func(t *testing.T) { c.run(t, h) }) {
			// Later checks build on earlier ones (the repositories, the
			// deployed file), so stop at the first failure.
			return
		}
	}
}

// harness is a running artifactweave and the clients that talk to it.
type harness struct {
	url         string
	password    string
	storageHost string
	// noFollow sees the registry's own answer, so a 302 stays a 302.
	noFollow *http.Client
	// plain follows redirects and carries no credentials, as a package
	// client does once it leaves the registry.
	plain  *http.Client
	cmd    *exec.Cmd
	stderr bytes.Buffer
}

func start(t *testing.T) *harness {
	t.Helper()
	bin := mustEnv(t, "ARTIFACTWEAVE_BIN")
	config := mustEnv(t, "ARTIFACTWEAVE_SCENARIO_CONFIG")
	h := &harness{
		storageHost: mustEnv(t, "ARTIFACTWEAVE_SCENARIO_STORAGE_HOST"),
		password:    randomString(t, 18),
		noFollow: &http.Client{
			Timeout:       10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		plain: &http.Client{Timeout: 10 * time.Minute},
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	h.url = "http://127.0.0.1:" + strconv.Itoa(port)
	dataDir := t.TempDir()
	// The scenario's output holds the blob section only; listen, data
	// directory and base URL come from flags.
	cfg := filepath.Join(dataDir, "artifactweave.yaml")
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h.cmd = exec.CommandContext(t.Context(), bin, "serve",
		"--config", cfg,
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--data-dir", filepath.Join(dataDir, "data"),
		"--base-url", h.url,
		"--log-format", "json",
	)
	h.cmd.Env = append(os.Environ(),
		"ARTIFACTWEAVE_MASTER_KEY=v1:"+randomString(t, 32),
		"ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD="+h.password,
	)
	h.cmd.Stderr = &h.stderr
	// The context cancels with a kill; SIGTERM first lets the server drain.
	h.cmd.Cancel = func() error { return h.cmd.Process.Signal(syscall.SIGTERM) }
	h.cmd.WaitDelay = 15 * time.Second
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
		_ = h.cmd.Wait()
		if t.Failed() {
			t.Logf("artifactweave stderr:\n%s", tail(h.stderr.String(), 8000))
		}
	})
	h.waitReady(t)
	return h
}

// waitReady allows a minute: opening a cloud store checks the bucket or
// container and, for GCS without a key, whether the credentials can sign.
func (h *harness) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		resp, err := h.do(t, http.MethodGet, "/artifactory/api/v1/system/readiness", nil, nil)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if h.cmd.ProcessState != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("artifactweave did not become ready; stderr:\n%s", tail(h.stderr.String(), 8000))
}

// do sends one request to the registry as the bootstrap admin, without
// following redirects.
func (h *harness) do(
	t *testing.T,
	method, path string,
	body []byte,
	header http.Header,
) (*http.Response, error) {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		reader = bytes.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, h.url+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.SetBasicAuth("admin", h.password)
	resp, err := h.noFollow.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

func mustEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set", name)
	}
	return v
}

func randomString(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func freePort() (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errNotTCP
	}
	return addr.Port, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
