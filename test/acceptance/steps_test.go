package acceptance

import (
	"archive/zip"
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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	stderr syncBuffer
	status int
	header http.Header
	body   []byte
	lastRC int
	// dir holds the files a scenario writes, for {dir} in jf arguments.
	dir string
	// next holds headers for the next request only.
	next map[string]string
}

// syncBuffer is the server's stderr: the process writes it from exec's
// copying goroutine while steps read the access log.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	sc.Step(`^I (PUT|POST) "([^"]*)" as admin with body:$`, w.sendAsAdmin)
	sc.Step(`^I DELETE "([^"]*)" as admin$`, w.deleteAsAdmin)
	sc.Step(`^I PATCH "([^"]*)" as admin with YAML:$`, w.patchYAMLAsAdmin)
	sc.Step(`^I PATCH "([^"]*)" as admin with JSON:$`, w.patchJSONAsAdmin)
	sc.Step(`^I (PUT|HEAD|POST) "([^"]*)" as admin$`, w.sendEmptyAsAdmin)
	sc.Step(`^the next request has header "([^"]*)" "([^"]*)"$`, w.nextHeader)
	sc.Step(`^a file "([^"]*)" of (\d+) bytes$`, w.writeSizedFile)
	sc.Step(`^the files "([^"]*)" and "([^"]*)" are identical$`, w.filesIdentical)
	sc.Step(`^the output contains "([^"]*)"$`, w.outputContains)
	sc.Step(`^the server answered (\d+) to ([A-Z]+) "([^"]*)"$`, w.serverAnswered)
	sc.Step(`^the server never answered (\d+) to ([A-Z]+) "([^"]*)"$`, w.serverNeverAnswered)
	sc.Step(`^a file "([^"]*)" with content:$`, w.writeFile)
	sc.Step(`^a zip file "([^"]*)" holding "([^"]*)" with content "([^"]*)"$`, w.writeZip)
	sc.Step(`^the response status is (\d+)$`, w.statusIs)
	sc.Step(`^the response body is "([^"]*)"$`, w.bodyIs)
	sc.Step(`^the response body contains "([^"]*)"$`, w.bodyContains)
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
		// A scenario that collects garbage collects what it has just released,
		// rather than waiting out the production grace period.
		"ARTIFACTWEAVE_GC_GRACE_PERIOD=1ms",
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

func (w *world) sendAsAdmin(method, path string, body *godog.DocString) error {
	return w.send(method, path, body.Content, func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
	})
}

// patchYAMLAsAdmin sends a configuration descriptor, which is YAML, not
// JSON.
func (w *world) patchYAMLAsAdmin(path string, body *godog.DocString) error {
	return w.send(http.MethodPatch, path, body.Content, func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
		r.Header.Set("Content-Type", "application/yaml")
	})
}

func (w *world) patchJSONAsAdmin(path string, body *godog.DocString) error {
	return w.sendAsAdmin(http.MethodPatch, path, body)
}

func (w *world) deleteAsAdmin(path string) error {
	return w.send(http.MethodDelete, path, "", func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
	})
}

func (w *world) sendEmptyAsAdmin(method, path string) error {
	return w.send(method, path, "", func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
	})
}

func (w *world) nextHeader(name, value string) error {
	if w.next == nil {
		w.next = map[string]string{}
	}
	w.next[name] = value
	return nil
}

// writeSizedFile writes size bytes of a repeating pattern, enough to pass
// the CLI's checksum-deploy and split-download thresholds.
func (w *world) writeSizedFile(name string, size int) error {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return w.writeFile(name, &godog.DocString{Content: string(b)})
}

func (w *world) filesIdentical(a, b string) error {
	x, err := os.ReadFile(filepath.Join(w.dir, a))
	if err != nil {
		return err
	}
	y, err := os.ReadFile(filepath.Join(w.dir, b))
	if err != nil {
		return err
	}
	if !bytes.Equal(x, y) {
		return fmt.Errorf("%s (%d bytes) and %s (%d bytes) differ", a, len(x), b, len(y))
	}
	return nil
}

func (w *world) outputContains(want string) error {
	if !strings.Contains(string(w.body), want) {
		return fmt.Errorf("output does not contain %q:\n%s", want, w.body)
	}
	return nil
}

// answers reads the server's JSON access log for the statuses it gave a
// method and path. Matrix parameters are ignored: on CI the JFrog CLI appends
// vcs.* build properties (";vcs.branch=...") to every upload URL, which a
// scenario cannot know in advance.
func (w *world) answers(method, path string) []int {
	var out []int
	for _, line := range strings.Split(w.stderr.String(), "\n") {
		var rec struct {
			Msg, Method, Path string
			Status            int
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Msg != "request" ||
			rec.Method != method {
			continue
		}
		if logged, _, _ := strings.Cut(rec.Path, ";"); logged == path {
			out = append(out, rec.Status)
		}
	}
	return out
}

func (w *world) serverAnswered(status int, method, path string) error {
	got := w.answers(method, path)
	for _, s := range got {
		if s == status {
			return nil
		}
	}
	return fmt.Errorf("%s %s was answered %v, not %d; requests logged: %v",
		method, path, got, status, w.requests(method))
}

// requests lists the logged requests of a method, for failure messages.
func (w *world) requests(method string) []string {
	var out []string
	for _, line := range strings.Split(w.stderr.String(), "\n") {
		var rec struct {
			Msg, Method, Path string
			Status            int
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == "request" &&
			rec.Method == method {
			out = append(out, fmt.Sprintf("%s=%d", rec.Path, rec.Status))
		}
	}
	return out
}

func (w *world) serverNeverAnswered(status int, method, path string) error {
	got := w.answers(method, path)
	for _, s := range got {
		if s == status {
			return fmt.Errorf("%s %s was answered %v, including %d", method, path, got, status)
		}
	}
	return nil
}

// writeFile writes a scenario file, such as a jf repository template, under
// the scenario's directory.
func (w *world) writeFile(name string, content *godog.DocString) error {
	if w.dir == "" {
		dir, err := os.MkdirTemp("", "artifactweave-files-")
		if err != nil {
			return err
		}
		w.t.Cleanup(func() { _ = os.RemoveAll(dir) })
		w.dir = dir
	}
	return os.WriteFile(filepath.Join(w.dir, name), []byte(content.Content), 0o600)
}

// writeZip writes a zip archive with one entry, for explode and archive
// entry scenarios (a docstring cannot hold binary content).
func (w *world) writeZip(name, entry, content string) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(entry)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, content); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return w.writeFile(name, &godog.DocString{Content: buf.String()})
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
	for k, v := range w.next {
		req.Header.Set(k, v)
	}
	w.next = nil
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

func (w *world) bodyContains(want string) error {
	if !strings.Contains(string(w.body), want) {
		return fmt.Errorf("body %q does not contain %q", w.body, want)
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
// base, {password} by the seeded admin's and {dir} by the scenario's file
// directory. JFROG_CLI_HOME_DIR isolates the CLI's config from the
// developer's.
func (w *world) runJF(args string) error {
	home, err := os.MkdirTemp("", "jfrog-home-")
	if err != nil {
		return err
	}
	w.t.Cleanup(func() { _ = os.RemoveAll(home) })
	argv := strings.Fields(strings.NewReplacer(
		"{url}", w.url+"/artifactory",
		"{password}", testAdminPassword,
		"{dir}", w.dir,
	).Replace(args))
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
