package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// replicationGate delays one real peer request. Every response comes from the
// separate application; cancellation never forwards a request after shutdown.
type replicationGate struct {
	mu               sync.Mutex
	method, path     string
	entered, release chan struct{}
	claimed          bool
}

func (g *replicationGate) arm(method, path string) (<-chan struct{}, func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.method, g.path, g.claimed = method, path, false
	g.entered, g.release = make(chan struct{}), make(chan struct{})
	release := g.release
	var once sync.Once
	return g.entered, func() { once.Do(func() { close(release) }) }
}

func (g *replicationGate) forward(proxy http.Handler, writer http.ResponseWriter, request *http.Request) {
	g.mu.Lock()
	matched := request.Method == g.method && request.URL.Path == g.path && !g.claimed
	entered, release := g.entered, g.release
	if matched {
		g.claimed = true
	}
	g.mu.Unlock()
	if matched {
		close(entered)
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
	}
	proxy.ServeHTTP(writer, request)
}

func (w *world) restartReplicationProcess(extra ...string) error {
	if w.server == nil {
		return fmt.Errorf("owned replication process is not running")
	}
	args, environment := slices.Clone(w.server.Args[1:]), slices.Clone(w.server.Env)
	w.stop()
	w.server = exec.Command(binary, args...)
	w.server.Env, w.server.Stderr = append(environment, extra...), &w.stderr
	if err := w.server.Start(); err != nil {
		return fmt.Errorf("restart owned replication process: %w", err)
	}
	return w.waitReady()
}

func (w *world) qualifyReplicationRecovery(direction string) (result error) {
	peer := w.replicationPeer
	if peer == nil {
		return fmt.Errorf("replication recovery requires two owned application processes")
	}
	origin, err := url.Parse(peer.url)
	if err != nil {
		return fmt.Errorf("owned peer origin: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(origin)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	defer transport.CloseIdleConnections()
	gate := &replicationGate{}
	link := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { gate.forward(proxy, writer, request) }))
	defer link.Close()
	// A single worker makes a second accepted request demonstrably pending.
	// Two attempts bound a deliberate real peer credential refusal in this lab.
	if err := w.restartReplicationProcess("ARTIFACTWEAVE_REMOTE_ALLOWED_PRIVATE_ORIGINS="+peer.url+","+link.URL,
		"ARTIFACTWEAVE_WORK_WORKERS=1", "ARTIFACTWEAVE_WORK_MAX_ATTEMPTS=2"); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, w.replicationRecoveryEvidence(direction)) }()
	fixture, err := w.configureReplicationPair(direction, "generic")
	if err != nil {
		return err
	}
	if direction == "push" {
		body, _ := json.Marshal(map[string]string{"url": link.URL + "/artifactory/" + fixture.consumerKey})
		if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replications/"+fixture.owningRepo, string(body), http.StatusOK); err != nil {
			return err
		}
	} else {
		body, _ := json.Marshal(map[string]string{"url": link.URL + "/artifactory/" + fixture.sourceKey})
		if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/repositories/"+fixture.owningRepo, string(body), http.StatusOK); err != nil {
			return err
		}
	}
	method := http.MethodPut
	peerKey := fixture.consumerKey
	if direction == "pull" {
		method, peerKey = http.MethodGet, fixture.sourceKey
	}
	gatePath := "/artifactory/" + peerKey + "/selected/b.bin"
	deploy := func(revision string) error {
		for _, path := range []string{"selected/a.bin", "selected/b.bin"} {
			if _, err := replicationFixtureRequest(
				fixture.upstream,
				http.MethodPut,
				"/artifactory/"+fixture.sourceKey+"/"+path,
				revision+" "+path,
				http.StatusCreated,
			); err != nil {
				return err
			}
		}
		return nil
	}
	execute := func(root string) error {
		_, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replication/execute/"+fixture.owningRepo+"/"+root, "[]", http.StatusAccepted)
		return err
	}
	awaitGate := func(entered <-chan struct{}) error {
		select {
		case <-entered:
			return nil
		case <-time.After(20 * time.Second):
			return fmt.Errorf("real replication transfer did not reach its interruption point")
		case <-w.t.Context().Done():
			return fmt.Errorf("replication recovery fixture cancelled")
		}
	}
	if err := deploy("before restart"); err != nil {
		return err
	}
	entered, release := gate.arm(method, gatePath)
	defer release()
	if err := execute("selected"); err != nil {
		return err
	}
	if err := awaitGate(entered); err != nil {
		return err
	}
	if err := w.waitReplicationState(fixture.owningRepo, "incomplete"); err != nil {
		return err
	}
	if err := replicationBytes(fixture.consumer, fixture.consumerKey, "selected/a.bin", "before restart selected/a.bin"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(fixture.consumer, http.MethodGet, "/artifactory/"+fixture.consumerKey+"/selected/b.bin", "", http.StatusNotFound); err != nil {
		return err
	}
	// SIGTERM cancels the in-flight request, releases unfinished queue work,
	// and flushes counters. Reopen the exact same SQL store, blobs and key.
	if err := w.restartReplicationProcess(); err != nil {
		return err
	}
	release()
	if err := w.waitReplicationState(fixture.owningRepo, "ok"); err != nil {
		return err
	}
	if err := replicationBytes(fixture.consumer, fixture.consumerKey, "selected/b.bin", "before restart selected/b.bin"); err != nil {
		return err
	}
	if err := deploy("after credential recovery"); err != nil {
		return err
	}
	entered, release = gate.arm(method, gatePath)
	defer release()
	if err := execute("selected"); err != nil {
		return err
	}
	if err := awaitGate(entered); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(peer, http.MethodDelete, "/artifactory/api/security/users/replication-transport", "", http.StatusOK); err != nil {
		return err
	}
	release()
	if err := w.waitReplicationState(fixture.owningRepo, "partial_failure"); err != nil {
		return err
	}
	if err := replicationBytes(fixture.consumer, fixture.consumerKey, "selected/a.bin", "after credential recovery selected/a.bin"); err != nil {
		return err
	}
	if err := replicationBytes(fixture.consumer, fixture.consumerKey, "selected/b.bin", "before restart selected/b.bin"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(peer, http.MethodPut, "/artifactory/api/security/users/replication-transport",
		`{"name":"replication-transport","email":"replication@example.invalid","password":"`+replicationTransportPassword+`","admin":false}`, http.StatusCreated); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(
		peer,
		http.MethodPut,
		"/artifactory/api/security/permissions/replication-transport",
		`{"name":"replication-transport","repositories":["`+peerKey+`"],"includesPattern":"**","principals":{"users":{"replication-transport":["r","w","n","d"]}}}`,
		http.StatusOK,
	); err != nil {
		return err
	}
	// Failed Basic logins intentionally suspend this user name briefly. Keep
	// that security behavior and prove a usable login before admitting retry.
	if err := peer.waitRestoredReplicationCredential("/artifactory/api/storage/" + peerKey + "/selected/a.bin"); err != nil {
		return err
	}
	if err := w.executeAndWaitReplication(fixture.owningRepo); err != nil {
		return fmt.Errorf("retry after restoring peer credentials: %w", err)
	}
	if err := replicationBytes(fixture.consumer, fixture.consumerKey, "selected/b.bin", "after credential recovery selected/b.bin"); err != nil {
		return err
	}
	// Hold a valid transfer on selected/, accept pending/ behind it, then
	// disable the configuration before that second run can enter a driver.
	if err := deploy("pending revocation"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(
		fixture.upstream,
		http.MethodPut,
		"/artifactory/"+fixture.sourceKey+"/pending/forbidden.bin",
		"must stay pending",
		http.StatusCreated,
	); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replications/"+fixture.owningRepo,
		`{"includePathPrefixPattern":"**"}`, http.StatusOK); err != nil {
		return err
	}
	entered, release = gate.arm(method, gatePath)
	defer release()
	if err := execute("selected"); err != nil {
		return err
	}
	if err := awaitGate(entered); err != nil {
		return err
	}
	if err := execute("pending"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replications/"+fixture.owningRepo, `{"enabled":false}`, http.StatusOK); err != nil {
		return err
	}
	release()
	if err := w.waitReplicationState(fixture.owningRepo, "error"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(fixture.consumer, http.MethodGet, "/artifactory/"+fixture.consumerKey+"/pending/forbidden.bin", "", http.StatusNotFound); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replications/"+fixture.owningRepo, `{"enabled":true}`, http.StatusOK); err != nil {
		return err
	}
	if err := w.executeAndWaitReplicationPath(fixture.owningRepo, "pending"); err != nil {
		return fmt.Errorf("new execution after re-enabling configuration: %w", err)
	}
	return replicationBytes(fixture.consumer, fixture.consumerKey, "pending/forbidden.bin", "must stay pending")
}

func (w *world) waitRestoredReplicationCredential(path string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		request, err := http.NewRequestWithContext(w.t.Context(), http.MethodGet, w.url+path, nil)
		if err != nil {
			return fmt.Errorf("restored credential readiness request: %w", err)
		}
		request.SetBasicAuth("replication-transport", replicationTransportPassword)
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("restored credential readiness transport: %w", err)
		}
		if err := response.Body.Close(); err != nil {
			return fmt.Errorf("close restored credential readiness response: %w", err)
		}
		if response.StatusCode == http.StatusOK {
			return nil
		}
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			return fmt.Errorf("restored peer credential readiness got%d", response.StatusCode)
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return fmt.Errorf("restored peer credential did not become usable within its deadline")
		case <-w.t.Context().Done():
			return fmt.Errorf("restored credential readiness cancelled")
		}
	}
}

func (w *world) waitReplicationState(repository, expected string) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		body, err := replicationFixtureRequest(w, http.MethodGet, "/artifactory/api/replication/"+repository, "", http.StatusOK)
		if err != nil {
			return err
		}
		var status struct {
			Status  string `json:"status"`
			Targets []struct {
				Status string `json:"status"`
			} `json:"targets"`
		}
		if json.Unmarshal(body, &status) != nil || strings.Contains(string(body), replicationTransportPassword) {
			return fmt.Errorf("recovery status is invalid or discloses credentials")
		}
		actual := status.Status
		if len(status.Targets) == 1 {
			actual = status.Targets[0].Status
		}
		if actual == expected {
			return nil
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return fmt.Errorf("replication recovery state got %q want %q", actual, expected)
		case <-w.t.Context().Done():
			return fmt.Errorf("replication recovery fixture cancelled")
		}
	}
}

func (w *world) replicationRecoveryEvidence(direction string) error {
	destination := os.Getenv("ARTIFACTWEAVE_REPLICATION_EVIDENCE_DIR")
	if destination == "" {
		return nil
	}
	backend := "sqlite"
	secrets := []string{testAdminPassword, testMasterKey, replicationTransportPassword}
	for _, instance := range []*world{w, w.replicationPeer} {
		for _, entry := range instance.env {
			if entry == "ARTIFACTWEAVE_DATABASE_DRIVER=postgres" {
				backend = "postgres"
			}
			if dsn, ok := strings.CutPrefix(entry, "ARTIFACTWEAVE_DATABASE_DSN="); ok && dsn != "" {
				secrets = append(secrets, dsn)
			}
		}
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create recovery evidence directory: %w", err)
	}
	for kind, content := range map[string]string{"source": w.stderr.String(), "peer": w.replicationPeer.stderr.String()} {
		path := filepath.Join(destination, "replication-"+backend+"-"+direction+"-recovery-"+kind+".log")
		if err := os.WriteFile(path, []byte(replicationSanitized(content, secrets)), 0o600); err != nil {
			return fmt.Errorf("retain sanitized recovery evidence: %w", err)
		}
	}
	return nil
}
