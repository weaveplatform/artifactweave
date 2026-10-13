package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

// testMasterKey is a fixed key so scenarios are reproducible; it protects
// nothing because the data directory is a temp dir deleted after the run.
const testMasterKey = "acceptance-master-key-0123456789abcdef0123456789abcdef"

// testAdminPassword is the seeded admin's password (bootstrap.admin_password).
const testAdminPassword = "acceptance-admin"

// world is one scenario's state.
type world struct {
	t      *testing.T
	url    string
	server *exec.Cmd
	stderr bytes.Buffer
	status int
	header http.Header
	body   []byte
	lastRC int
}

func newWorld(t *testing.T) *world { return &world{t: t} }

// register binds every step.
func (w *world) register(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		w.stop()
		return ctx, err
	})
	sc.Step(`^the registry is running$`, w.start)
	sc.Step(`^I GET "([^"]*)"$`, w.get)
	sc.Step(`^I GET "([^"]*)" as admin$`, w.getAsAdmin)
	sc.Step(`^I GET "([^"]*)" as "([^"]*)" with password "([^"]*)"$`, w.getAs)
	sc.Step(`^the response status is not (\d+)$`, w.statusIsNot)
	sc.Step(`^the response header "([^"]*)" contains "([^"]*)"$`, w.headerContains)
	sc.Step(`^I POST "([^"]*)" with body:$`, w.post)
	sc.Step(`^the response status is (\d+)$`, w.statusIs)
	sc.Step(`^the response body is "([^"]*)"$`, w.bodyIs)
	sc.Step(`^the JSON field "([^"]*)" equals "([^"]*)"$`, w.jsonFieldEquals)
	sc.Step(`^the JSON field "([^"]*)" is not empty$`, w.jsonFieldNotEmpty)
	sc.Step(`^the JSON field "([^"]*)" exists$`, w.jsonFieldExists)
	sc.Step(`^the JFrog CLI is available$`, w.jfAvailable)
	sc.Step(`^I run jf "([^"]*)"$`, w.runJF)
	sc.Step(`^the command succeeds$`, w.commandSucceeds)
}

// start launches the binary on a free port and waits for readiness.
func (w *world) start() error {
	port, err := freePort()
	if err != nil {
		return err
	}
	dataDir, err := os.MkdirTemp("", "artifactweave-data-")
	if err != nil {
		return err
	}
	w.t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	w.url = "http://127.0.0.1:" + strconv.Itoa(port)
	w.server = exec.Command(
		binary,
		"serve",
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--data-dir", dataDir,
		"--base-url", w.url,
		"--log-format", "json",
	)
	w.server.Env = append(os.Environ(),
		"ARTIFACTWEAVE_MASTER_KEY="+testMasterKey,
		"ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD="+testAdminPassword,
	)
	if coverDir != "" {
		w.server.Env = append(w.server.Env, "GOCOVERDIR="+coverDir)
	}
	w.server.Stderr = &w.stderr
	if err := w.server.Start(); err != nil {
		return err
	}
	return w.waitReady()
}

func (w *world) waitReady() error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(w.url + "/artifactory/api/v1/system/readiness")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if w.server.ProcessState != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("artifactweave did not become ready; stderr:\n%s", w.stderr.String())
}

// stop sends SIGTERM and waits so the binary flushes its coverage counters;
// a plain kill would lose them.
func (w *world) stop() {
	if w.server == nil || w.server.Process == nil {
		return
	}
	_ = w.server.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = w.server.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = w.server.Process.Kill()
		<-done
	}
	w.server = nil
}

func (w *world) get(path string) error {
	return w.request(http.MethodGet, path, "")
}

func (w *world) getAsAdmin(path string) error {
	return w.getAs(path, "admin", testAdminPassword)
}

func (w *world) getAs(path, user, password string) error {
	return w.send(
		http.MethodGet,
		path,
		"",
		func(r *http.Request) { r.SetBasicAuth(user, password) },
	)
}

// post takes the body as a Gherkin docstring: JSON in an inline quoted
// argument would need escaping that godog's step regex cannot match.
func (w *world) post(path string, body *godog.DocString) error {
	return w.request(http.MethodPost, path, body.Content)
}

func (w *world) request(method, path, body string) error {
	return w.send(method, path, body, func(*http.Request) {})
}

func (w *world) send(method, path, body string, prepare func(*http.Request)) error {
	req, err := http.NewRequestWithContext(
		context.Background(),
		method,
		w.url+path,
		strings.NewReader(body),
	)
	if err != nil {
		return err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	prepare(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	w.status, w.header = resp.StatusCode, resp.Header
	w.body, err = io.ReadAll(resp.Body)
	return err
}

func (w *world) statusIs(want int) error {
	if w.status != want {
		return fmt.Errorf("status %d, want %d; body: %s", w.status, want, w.body)
	}
	return nil
}

func (w *world) statusIsNot(unwanted int) error {
	if w.status == unwanted {
		return fmt.Errorf("status %d; body: %s", w.status, w.body)
	}
	return nil
}

func (w *world) headerContains(name, want string) error {
	if got := w.header.Get(name); !strings.Contains(got, want) {
		return fmt.Errorf("header %s is %q, want it to contain %q", name, got, want)
	}
	return nil
}

func (w *world) bodyIs(want string) error {
	if got := strings.TrimSpace(string(w.body)); got != want {
		return fmt.Errorf("body %q, want %q", got, want)
	}
	return nil
}

// jsonField walks a dotted key with numeric segments for array indexes,
// so "errors.0.status" reads the Artifactory envelope.
func (w *world) jsonField(key string) (any, error) {
	var doc any
	if err := json.Unmarshal(w.body, &doc); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w; body: %s", err, w.body)
	}
	cur := doc
	for _, seg := range strings.Split(key, ".") {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, fmt.Errorf("field %q not found in %s", key, w.body)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil, fmt.Errorf("index %q invalid for %q in %s", seg, key, w.body)
			}
			cur = v[i]
		default:
			return nil, fmt.Errorf("cannot descend into %q at %q", key, seg)
		}
	}
	return cur, nil
}

func (w *world) jsonFieldEquals(key, want string) error {
	v, err := w.jsonField(key)
	if err != nil {
		return err
	}
	if got := fmt.Sprint(v); got != want {
		return fmt.Errorf("field %q is %q, want %q", key, got, want)
	}
	return nil
}

func (w *world) jsonFieldNotEmpty(key string) error {
	v, err := w.jsonField(key)
	if err != nil {
		return err
	}
	if fmt.Sprint(v) == "" {
		return fmt.Errorf("field %q is empty", key)
	}
	return nil
}

func (w *world) jsonFieldExists(key string) error {
	_, err := w.jsonField(key)
	return err
}

// jfAvailable skips the scenario when no CLI binary is configured, so the
// @jf features pass locally without the download the CI job performs.
func (w *world) jfAvailable() error {
	if os.Getenv("ARTIFACTWEAVE_JF_BIN") == "" {
		return godog.ErrSkip
	}
	return nil
}

// runJF runs the JFrog CLI with {url} replaced by the registry's Artifactory
// base. JFROG_CLI_HOME_DIR isolates the CLI's config from the developer's.
func (w *world) runJF(args string) error {
	home, err := os.MkdirTemp("", "jfrog-home-")
	if err != nil {
		return err
	}
	w.t.Cleanup(func() { _ = os.RemoveAll(home) })
	argv := strings.Fields(strings.ReplaceAll(args, "{url}", w.url+"/artifactory"))
	cmd := exec.Command(os.Getenv("ARTIFACTWEAVE_JF_BIN"), argv...)
	cmd.Env = append(
		os.Environ(),
		"JFROG_CLI_HOME_DIR="+home,
		"JFROG_CLI_REPORT_USAGE=false",
		"CI=true",
	)
	out, err := cmd.CombinedOutput()
	w.body = out
	w.lastRC = 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		w.lastRC = exitErr.ExitCode()
		return nil
	}
	return err
}

func (w *world) commandSucceeds() error {
	if w.lastRC != 0 {
		return fmt.Errorf("exit code %d; output:\n%s", w.lastRC, w.body)
	}
	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("listener address is not TCP")
	}
	return addr.Port, nil
}
