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
//	ARTIFACTWEAVE_SCENARIO_OUTPUTS       every output, as terraform output -json writes them
//	ARTIFACTWEAVE_BIN                    the artifactweave binary built in the job
package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
// by ARTIFACTWEAVE_SCENARIO. Scenarios register theirs from an init function
// in their own file: the mirror's copies, drain and repair (mirror_test.go),
// the cloud's own replica (replica_test.go), the real edge (cdn_test.go).
var scenarioChecks = map[string][]check{}

// scenarioReady holds what a scenario must wait for before any check, keyed
// like scenarioChecks: an edge that comes up after apply returns, such as
// Cloud CDN's managed certificate and load balancer (cdn_test.go).
var scenarioReady = map[string]func(t *testing.T, h *harness){}

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
	if ready := scenarioReady[scenario]; ready != nil {
		ready(t, h)
	}
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
	url      string
	password string
	// storageHosts are the hosts a downloadRedirect may land on: the
	// scenario's storage_host and, for a mirror, every member's.
	storageHosts map[string]bool
	// outputs are the scenario's Terraform outputs, by name.
	outputs map[string]json.RawMessage
	// noFollow sees the registry's own answer, so a 302 stays a 302.
	noFollow *http.Client
	// plain follows redirects and carries no credentials, as a package
	// client does once it leaves the registry.
	plain *http.Client
	// What a restart keeps: the binary, the configuration, the data
	// directory and the master key that seals it, and the test's context,
	// which outlives the subtest that restarts.
	ctx       context.Context
	bin       string
	config    string
	dataDir   string
	masterKey string
	cmd       *exec.Cmd
	stderr    bytes.Buffer
}

func start(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		storageHosts: map[string]bool{mustEnv(t, "ARTIFACTWEAVE_SCENARIO_STORAGE_HOST"): true},
		outputs:      map[string]json.RawMessage{},
		password:     randomString(t, 18),
		noFollow: &http.Client{
			Timeout:       10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		plain:     &http.Client{Timeout: 10 * time.Minute},
		ctx:       t.Context(),
		bin:       mustEnv(t, "ARTIFACTWEAVE_BIN"),
		dataDir:   t.TempDir(),
		masterKey: "v1:" + randomString(t, 32),
	}
	h.readOutputs(t)
	for _, m := range h.members(t) {
		h.storageHosts[strings.SplitN(m.SignedURLPrefix, "/", 2)[0]] = true
	}
	// The scenario's output holds the blob section only; listen, data
	// directory and base URL come from flags.
	h.config = filepath.Join(h.dataDir, "artifactweave.yaml")
	raw, err := os.ReadFile(mustEnv(t, "ARTIFACTWEAVE_SCENARIO_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.stop()
		if t.Failed() {
			t.Logf("artifactweave stderr:\n%s", tail(h.stderr.String(), 8000))
		}
	})
	h.launch(t, nil)
	return h
}

// launch starts artifactweave on a free port with the harness's data
// directory, plus env, and waits until it is ready.
func (h *harness) launch(t *testing.T, env []string) {
	t.Helper()
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	h.url = "http://127.0.0.1:" + strconv.Itoa(port)
	h.cmd = exec.CommandContext(h.ctx, h.bin, "serve",
		"--config", h.config,
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--data-dir", filepath.Join(h.dataDir, "data"),
		"--base-url", h.url,
		"--log-format", "json",
	)
	h.cmd.Env = append(append(os.Environ(),
		"ARTIFACTWEAVE_MASTER_KEY="+h.masterKey,
		"ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD="+h.password,
	), env...)
	h.cmd.Stderr = &h.stderr
	// The context cancels with a kill; SIGTERM first lets the server drain.
	cmd := h.cmd
	h.cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	h.cmd.WaitDelay = 15 * time.Second
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitReady(t)
}

// stop ends the running artifactweave, letting it drain.
func (h *harness) stop() {
	if h.cmd == nil || h.cmd.Process == nil || h.cmd.ProcessState != nil {
		return
	}
	_ = h.cmd.Process.Signal(syscall.SIGTERM)
	_ = h.cmd.Wait()
}

// restart stops artifactweave and starts it again on the same data, with
// env added to its environment (a drained mirror member, for one).
func (h *harness) restart(t *testing.T, env ...string) {
	t.Helper()
	h.stop()
	h.launch(t, env)
}

// readOutputs loads ARTIFACTWEAVE_SCENARIO_OUTPUTS, when the workflow set it,
// as name to value.
func (h *harness) readOutputs(t *testing.T) {
	t.Helper()
	path := os.Getenv("ARTIFACTWEAVE_SCENARIO_OUTPUTS")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for name, o := range all {
		h.outputs[name] = o.Value
	}
}

// mirrorMember is one entry of a mirror scenario's mirror_members output.
type mirrorMember struct {
	Store           string `json:"store"`
	Zone            string `json:"zone"`
	SignedURLPrefix string `json:"signed_url_prefix"`
}

// members returns the scenario's mirror members, this instance's zone first;
// none for a scenario that is not a mirror.
func (h *harness) members(t *testing.T) []mirrorMember {
	t.Helper()
	raw, ok := h.outputs["mirror_members"]
	if !ok {
		return nil
	}
	var ms []mirrorMember
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatalf("parse mirror_members: %v", err)
	}
	return ms
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
