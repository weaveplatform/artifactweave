package acceptance

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken/idtokentest"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
)

// testMasterKey is a fixed key so scenarios are reproducible; it protects
// nothing because the data directory is a temp dir deleted after the run.
const testMasterKey = "acceptance-master-key-0123456789abcdef0123456789abcdef"

// testAdminPassword is the seeded admin's password (bootstrap.admin_password).
const testAdminPassword = "acceptance-admin"

// aqlCapture, when set, is a directory that receives every AQL query the
// server is sent, one file per query named after its scenario, so the JFrog
// CLI's real queries can refresh pkg/aql/aqltest/testdata (make capture-aql).
var aqlCapture = os.Getenv("ARTIFACTWEAVE_AQL_CAPTURE")

// world is one scenario's state.
type world struct {
	remote    *fakeupstream.Server
	smartPeer *smartPeerFixture
	t         *testing.T
	url       string
	server    *exec.Cmd
	stderr    syncBuffer
	status    int
	header    http.Header
	body      []byte
	lastRC    int
	// dataDir is the running server's data directory.
	dataDir string
	// dir holds the files a scenario writes, for {dir} in jf arguments.
	dir string
	// next holds headers for the next request only.
	next map[string]string
	// jfHome is the JFrog CLI's configuration for the scenario, so a
	// server added by jf c add is there for the commands after it.
	jfHome string
	// jfEnv is added to the JFrog CLI's environment for the rest of the
	// scenario, such as JFROG_RUN_NATIVE, without which jf docker build
	// collects no build-info.
	jfEnv []string
	// apmEnv supplies the native APM client's per-scenario registry credential.
	apmEnv []string
	// vars holds values kept from command output, for {name} in jf
	// arguments.
	vars map[string]string
	// issuer is the scenario's fake OIDC provider, nil until a step starts
	// it; its URL is {issuer}.
	issuer *idtokentest.Issuer
	// env is added to the server's environment when it starts, for
	// scenarios that configure another blob store.
	env []string
	// edge is the scenario's fake CDN edge, nil unless a step starts it.
	edge *cdntest.Edge
	// closers release what steps started (containers, the edge), after
	// the server has stopped.
	closers []func()
	// dockerConfig is the scenario's docker configuration, which docker
	// login and crane write their credentials to.
	dockerConfig string
	// goProxy is the GOPROXY the go command uses in this scenario, and
	// tlsCA the certificate of the scenario's TLS front, if it has one.
	goProxy, tlsCA string
	// npmImage, mavenImage, pythonImage and dotnetImage are the client
	// images the scenario chose with "the <client> client can reach the
	// registry", empty until then.
	npmImage, mavenImage, pythonImage, luarocksImage, dotnetImage, alpineImage, debianImage string
	alpineKey                                                                               *rsa.PrivateKey
	debianPrivate                                                                           string
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
	sc.Step(
		`^the client PDBs in the response resolve from NuGet repository "([^"]*)"$`,
		w.nugetSymbolsResolve,
	)
	sc.Step(`^the captured NuGet symbol URLs no longer resolve$`, w.nugetSymbolsRemoved)
	sc.After(func(ctx context.Context, s *godog.Scenario, err error) (context.Context, error) {
		w.stop()
		if w.issuer != nil {
			w.issuer.Close()
			w.issuer = nil
		}
		for i := len(w.closers) - 1; i >= 0; i-- {
			w.closers[i]()
		}
		w.closers, w.edge, w.env = nil, nil, nil
		if aqlCapture != "" {
			err = errors.Join(err, w.captureAQL(s.Name))
		}
		return ctx, err
	})
	w.registerCDN(sc)
	w.registerCache(sc)
	w.registerRemote(sc)
	w.registerMavenRemote(sc)
	w.registerDocker(sc)
	w.registerClients(sc)
	w.registerDebian(sc)
	w.registerLuaRocks(sc)
	w.registerAlpine(sc)
	w.registerPyPI(sc)
	w.registerHelm(sc)
	w.registerNuGet(sc)
	w.registerRemoteNuGet(sc)
	w.registerLegacyRemoteNuGet(sc)
	w.registerTerraformProviders(sc)
	w.registerTerraformBackend(sc)
	w.registerTerraformRegistry(sc)
	w.registerTerraformRemote(sc)
	w.registerTerraformProviderRemote(sc)
	w.registerPackageWiring(sc)
	w.registerWaits(sc)
	sc.Step(`^the registry is running$`, w.start)
	sc.Step(`^the registry is running with environment "([^"]*)"$`, w.startWith)
	sc.Step(`^I GET "([^"]*)"$`, w.get)
	sc.Step(`^I GET "([^"]*)" as admin$`, w.getAsAdmin)
	sc.Step(`^I GET "([^"]*)" as admin until the response status is (\d+)$`, w.getAsAdminUntil)
	sc.Step(`^I GET "([^"]*)" as "([^"]*)" with password "([^"]*)"$`, w.getAs)
	sc.Step(`^I GET "([^"]*)" with the kept token "([^"]*)"$`, w.getWithToken)
	sc.Step(`^the response status is not (\d+)$`, w.statusIsNot)
	sc.Step(`^the response header "([^"]*)" contains "([^"]*)"$`, w.headerContains)
	sc.Step(`^I keep the path of the response header "([^"]*)" as "([^"]*)"$`, w.keepHeaderPath)
	sc.Step(`^I POST "([^"]*)" with body:$`, w.post)
	sc.Step(`^I POST "([^"]*)" with a body of (\d+) bytes$`, w.postSized)
	sc.Step(`^I (PUT|POST|PATCH|DELETE) "([^"]*)" as admin with body:$`, w.sendAsAdmin)
	sc.Step(`^I DELETE "([^"]*)" as admin$`, w.deleteAsAdmin)
	sc.Step(`^I create the RSA key pair "([^"]*)" with alias "([^"]*)" as admin$`, w.createKeyPair)
	sc.Step(`^I add a generated client certificate "([^"]*)" as admin$`, w.addCertificate)
	sc.Step(`^I PATCH "([^"]*)" as admin with YAML:$`, w.patchYAMLAsAdmin)
	sc.Step(`^I PATCH "([^"]*)" as admin with JSON:$`, w.patchJSONAsAdmin)
	sc.Step(`^I (PUT|HEAD|POST) "([^"]*)" as admin$`, w.sendEmptyAsAdmin)
	sc.Step(`^the next request has header "([^"]*)" "([^"]*)"$`, w.nextHeader)
	sc.Step(`^a file "([^"]*)" of (\d+) bytes$`, w.writeSizedFile)
	sc.Step(`^the files "([^"]*)" and "([^"]*)" are identical$`, w.filesIdentical)
	sc.Step(`^the file "([^"]*)" does not exist$`, w.fileAbsent)
	sc.Step(`^the output contains "([^"]*)"$`, w.outputContains)
	sc.Step(`^the output does not contain "([^"]*)"$`, w.outputLacks)
	sc.Step(`^the server answered (\d+) to ([A-Z]+) "([^"]*)"$`, w.serverAnswered)
	sc.Step(`^the server never answered (\d+) to ([A-Z]+) "([^"]*)"$`, w.serverNeverAnswered)
	sc.Step(`^a file "([^"]*)" with content:$`, w.writeFile)
	sc.Step(`^a zip file "([^"]*)" holding "([^"]*)" with content "([^"]*)"$`, w.writeZip)
	sc.Step(`^the response status is (\d+)$`, w.statusIs)
	sc.Step(`^the response body is "([^"]*)"$`, w.bodyIs)
	sc.Step(`^the response body contains "([^"]*)"$`, w.bodyContains)
	// The response and a command's output share one buffer.
	sc.Step(`^the response body does not contain "([^"]*)"$`, w.outputLacks)
	sc.Step(`^the JSON field "([^"]*)" equals "([^"]*)"$`, w.jsonFieldEquals)
	sc.Step(`^the JSON field "([^"]*)" is not empty$`, w.jsonFieldNotEmpty)
	sc.Step(`^the JSON field "([^"]*)" exists$`, w.jsonFieldExists)
	sc.Step(`^the JSON field "([^"]*)" has (\d+) entries$`, w.jsonFieldLen)
	sc.Step(`^the JSON field "([^"]*)" is the number (-?\d+)$`, w.jsonFieldNumber)
	sc.Step(`^the JSON field "([^"]*)" does not exist$`, w.jsonFieldAbsent)
	sc.Step(`^the JFrog CLI is available$`, w.jfAvailable)
	sc.Step(`^I run jf "([^"]*)"$`, w.runJF)
	sc.Step(`^I run jf "([^"]*)" in "([^"]*)"$`, w.runJFIn)
	sc.Step(`^the JFrog CLI runs with "([^"]*)" set to "([^"]*)"$`, w.setJFEnv)
	sc.Step(`^the native APM CLI is available$`, w.apmAvailable)
	sc.Step(`^the APM CLI runs with "([^"]*)" set to "([^"]*)"$`, w.setAPMEnv)
	sc.Step(`^I run apm "([^"]*)" in "([^"]*)"$`, w.runAPMIn)
	sc.Step(`^the command succeeds$`, w.commandSucceeds)
	sc.Step(`^the command fails$`, w.commandFails)
	sc.Step(
		`^I keep the JSON field "([^"]*)" of the (?:output|response) as "([^"]*)"$`,
		w.keepOutputField,
	)
	sc.Step(`^the jf server "([^"]*)" has the refresh token kept as "([^"]*)"$`, w.jfRefreshToken)
	sc.Step(`^the access token kept as "([^"]*)" has expired$`, w.awaitExpiry)
	sc.Step(`^an OIDC provider is running$`, w.startIssuer)
	sc.Step(
		`^the OIDC provider issues an ID token for audience "([^"]*)" kept as "([^"]*)" with claims:$`,
		w.mintIDToken,
	)
	sc.Step(
		`^I import the jf server "([^"]*)" with the access token kept as "([^"]*)" `+
			`and the refresh token kept as "([^"]*)"$`,
		w.jfImport,
	)
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
	w.dataDir = dataDir
	w.url = "http://127.0.0.1:" + strconv.Itoa(port)
	w.server = exec.Command(
		binary,
		"serve",
		"--listen", "127.0.0.1:"+strconv.Itoa(port),
		"--data-dir", dataDir,
		"--base-url", w.url,
		"--log-format", "json",
	)
	if aqlCapture != "" {
		w.server.Args = append(w.server.Args, "--log-level", "debug")
	}
	w.server.Env = append(os.Environ(),
		"ARTIFACTWEAVE_MASTER_KEY="+testMasterKey,
		"ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD="+testAdminPassword,
		// A scenario that collects garbage collects what it has just released,
		// rather than waiting out the production grace period.
		"ARTIFACTWEAVE_GC_GRACE_PERIOD=1ms",
		// Scenarios mint their first token with the admin's password, as an
		// operator who automates with Basic credentials does once they turn
		// this on (Artifactory's "Enable Token Generation via API"). The
		// basic_auth_tokens feature runs with the default instead.
		"ARTIFACTWEAVE_TOKEN_ALLOW_BASIC_AUTH=true",
	)
	w.server.Env = append(w.server.Env, w.env...)
	if coverDir != "" {
		w.server.Env = append(w.server.Env, "GOCOVERDIR="+coverDir)
	}
	w.server.Stderr = &w.stderr
	if err := w.server.Start(); err != nil {
		return err
	}
	return w.waitReady()
}

// startWith starts the binary with one more NAME=value in its environment.
// It comes after start's own, and exec keeps the last value of a repeated
// name, so it can also undo one of start's settings.
func (w *world) startWith(pair string) error {
	w.env = append(w.env, pair)
	return w.start()
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

// queuedWait bounds the wait for work the server queues, such as an npm
// conversion, which its workers take within a poll of a second.
const queuedWait = 30 * time.Second

// getAsAdminUntil asks again until the answer is want, for what the server
// does after it answers.
func (w *world) getAsAdminUntil(path string, want int) error {
	deadline := time.Now().Add(queuedWait)
	for {
		if err := w.getAsAdmin(path); err != nil || w.status == want {
			return err
		}
		if time.Now().After(deadline) {
			return w.statusIs(want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (w *world) getAs(path, user, password string) error {
	return w.send(
		http.MethodGet,
		w.expand(path),
		"",
		func(r *http.Request) { r.SetBasicAuth(user, password) },
	)
}

// getWithToken sends a token kept from an earlier response as Bearer.
func (w *world) getWithToken(path, name string) error {
	tok, ok := w.vars[name]
	if !ok {
		return fmt.Errorf("no token kept as %q", name)
	}
	return w.send(http.MethodGet, path, "", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	})
}

// post takes the body as a Gherkin docstring: JSON in an inline quoted
// argument would need escaping that godog's step regex cannot match.
func (w *world) post(path string, body *godog.DocString) error {
	return w.request(http.MethodPost, path, w.expand(body.Content))
}

// postSized posts a body too long to write out in a feature, for the body
// limits.
func (w *world) postSized(path string, size int) error {
	return w.request(http.MethodPost, path, strings.Repeat("x", size))
}

func (w *world) sendAsAdmin(method, path string, body *godog.DocString) error {
	return w.send(method, w.expand(path), w.expand(body.Content), func(r *http.Request) {
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
	return w.send(http.MethodDelete, w.expand(path), "", func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
	})
}

func (w *world) sendEmptyAsAdmin(method, path string) error {
	return w.send(method, w.expand(path), "", func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
	})
}

func (w *world) nextHeader(name, value string) error {
	if w.next == nil {
		w.next = map[string]string{}
	}
	w.next[name] = w.expand(value)
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

func (w *world) fileAbsent(name string) error {
	if _, err := os.Stat(filepath.Join(w.dir, name)); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s exists (%v)", name, err)
	}
	return nil
}

func (w *world) outputContains(want string) error {
	if !strings.Contains(string(w.body), want) {
		return fmt.Errorf("output does not contain %q:\n%s", want, w.body)
	}
	return nil
}

func (w *world) outputLacks(unwanted string) error {
	if strings.Contains(string(w.body), unwanted) {
		return fmt.Errorf("output contains %q:\n%s", unwanted, w.body)
	}
	return nil
}

// captureAQL writes the AQL queries the server logged at debug level to
// aqlCapture, numbered in the order they arrived.
func (w *world) captureAQL(scenario string) error {
	slug := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(scenario))
	if err := os.MkdirAll(aqlCapture, 0o750); err != nil {
		return err
	}
	n := 0
	for _, line := range strings.Split(w.stderr.String(), "\n") {
		var rec struct{ Msg, Query string }
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Msg != "aql query" {
			continue
		}
		n++
		name := filepath.Join(aqlCapture, fmt.Sprintf("%s-%02d.aql", slug, n))
		if err := os.WriteFile(name, []byte(rec.Query), 0o600); err != nil {
			return err
		}
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
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content.Content), 0o600)
}

// filesDir is the scenario's file directory, created on first use and
// removed after the test.
func (w *world) filesDir() (string, error) {
	if w.dir == "" {
		dir, err := os.MkdirTemp("", "artifactweave-files-")
		if err != nil {
			return "", err
		}
		w.t.Cleanup(func() { _ = os.RemoveAll(dir) })
		w.dir = dir
	}
	return w.dir, nil
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
		return fmt.Errorf("status %d, want %d; body: %s%s", w.status, want, w.body, w.causes())
	}
	return nil
}

func (w *world) statusIsNot(unwanted int) error {
	if w.status == unwanted {
		return fmt.Errorf("status %d; body: %s%s", w.status, w.body, w.causes())
	}
	return nil
}

// causes returns, after a 5xx, the server log's ERROR and WARN lines. The
// body of a 500 says only "internal error"; without these a failure that
// does not reproduce leaves nothing to go on.
func (w *world) causes() string {
	if w.status < http.StatusInternalServerError {
		return ""
	}
	// The access log line reaches w.stderr through exec's copying goroutine,
	// so it can trail the response by a moment.
	id := w.header.Get("X-Request-ID")
	logged := func() bool { return id != "" && strings.Contains(w.stderr.String(), id) }
	for deadline := time.Now().Add(2 * time.Second); !logged() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	var b strings.Builder
	if !logged() {
		fmt.Fprintf(&b, "\nthe server's log has no line for request %q", id)
	}
	for line := range strings.Lines(w.stderr.String()) {
		if strings.Contains(line, `"level":"ERROR"`) || strings.Contains(line, `"level":"WARN"`) {
			b.WriteString("\n" + strings.TrimSpace(line))
		}
	}
	if b.Len() == 0 {
		return "\nthe server logged no error"
	}
	return "\nserver log:" + b.String()
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

// jsonFieldLen checks the length of an array field.
func (w *world) jsonFieldLen(key string, want int) error {
	v, err := w.jsonField(key)
	if err != nil {
		return err
	}
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("field %q is not an array: %v", key, v)
	}
	if len(list) != want {
		return fmt.Errorf("field %q has %d entries, want %d: %v", key, len(list), want, list)
	}
	return nil
}

// jsonFieldNumber checks that a field is a JSON number with the value want.
// "equals" cannot tell them apart: it prints a number and a string holding
// the same digits alike, and a client that decodes into an integer accepts
// only the number.
func (w *world) jsonFieldNumber(key string, want int) error {
	v, err := w.jsonField(key)
	if err != nil {
		return err
	}
	if n, ok := v.(float64); !ok || n != float64(want) {
		return fmt.Errorf("field %q is %#v, want the number %d", key, v, want)
	}
	return nil
}

// jsonFieldAbsent checks that a field is not in the response, which must be
// JSON: any body that is not would otherwise pass.
func (w *world) jsonFieldAbsent(key string) error {
	if !json.Valid(w.body) {
		return fmt.Errorf("body is not JSON: %s", w.body)
	}
	if v, err := w.jsonField(key); err == nil {
		return fmt.Errorf("field %q is %v, want it absent", key, v)
	}
	return nil
}

// jfAvailable skips the scenario when no CLI binary is configured, so the
// @jf features pass locally without the download the CI job performs.
func (w *world) jfAvailable() error {
	if os.Getenv("ARTIFACTWEAVE_JF_BIN") == "" {
		return godog.ErrSkip
	}
	return nil
}

func (w *world) apmAvailable() error {
	if os.Getenv("ARTIFACTWEAVE_APM_BIN") == "" {
		return godog.ErrSkip
	}
	return nil
}

func (w *world) setAPMEnv(name, value string) error {
	w.apmEnv = append(w.apmEnv, name+"="+w.expand(value))
	return nil
}

// runAPMIn isolates the native client's user configuration and cache while
// keeping its experimental registry setting across this scenario's commands.
func (w *world) runAPMIn(args, sub string) error {
	home := filepath.Join(w.dir, "apm-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	argv := strings.Fields(w.expand(args))
	cmd := exec.Command(os.Getenv("ARTIFACTWEAVE_APM_BIN"), argv...)
	cmd.Dir = filepath.Join(w.dir, sub)
	cmd.Env = append(os.Environ(), "HOME="+home, "CI=true")
	cmd.Env = append(cmd.Env, w.apmEnv...)
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

// expand replaces {url} with the registry's Artifactory base, {platform}
// with its platform base, {host} with its host and port as a registry
// client names it, {password} with the seeded admin's, {authurl} with the
// Artifactory base carrying the admin's credentials, {dir} with the
// scenario's file directory and {name} with a value kept from earlier
// output.
func (w *world) expand(s string) string {
	pairs := []string{
		"{url}", w.url + "/artifactory",
		// For clients that take credentials in a URL: pip's index-url, a
		// wheel's URL for uv, and terraform's HTTP module source.
		"{authurl}", "http://admin:" + testAdminPassword + "@" +
			strings.TrimPrefix(w.url, "http://") + "/artifactory",
		"{platform}", w.url,
		"{host}", strings.TrimPrefix(w.url, "http://"),
		"{password}", testAdminPassword,
		"{dir}", w.dir,
		// npm's _auth for the seeded admin.
		"{basicauth}", base64.StdEncoding.EncodeToString([]byte("admin:" + testAdminPassword)),
	}
	for k, v := range w.vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// jfRefreshToken gives a configured jf server a refresh token. The CLI
// keeps an Access refresh token only from a web login, which a test cannot
// drive (jf c import files a refresh token as the legacy Artifactory one),
// so it is written into the configuration file the CLI reads.
func (w *world) jfRefreshToken(serverID, name string) error {
	path := filepath.Join(w.jfHome, "jfrog-cli.conf.v6")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var conf map[string]any
	if err := json.Unmarshal(raw, &conf); err != nil {
		return err
	}
	servers, _ := conf["servers"].([]any)
	for _, s := range servers {
		if srv, ok := s.(map[string]any); ok && srv["serverId"] == serverID {
			srv["refreshToken"] = w.vars[name]
			out, err := json.MarshalIndent(conf, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(path, out, 0o600)
		}
	}
	return fmt.Errorf("no jf server %q in %s", serverID, raw)
}

// jfImport runs jf c import with a server configuration token: base64 of
// the JSON jf c export writes. The CLI keeps its refreshToken as the legacy
// Artifactory refresh token, which it refreshes through
// /artifactory/api/security/token.
func (w *world) jfImport(serverID, access, refresh string) error {
	conf, err := json.Marshal(map[string]any{
		"version": 2, "url": w.url + "/", "serverId": serverID,
		"accessToken": w.vars[access], "refreshToken": w.vars[refresh],
	})
	if err != nil {
		return err
	}
	return w.runJF("c import " + base64.StdEncoding.EncodeToString(conf))
}

// awaitExpiry waits until a kept access token's exp has passed, so the next
// use of it is of an expired token.
func (w *world) awaitExpiry(name string) error {
	parts := strings.Split(w.vars[name], ".")
	if len(parts) != 3 {
		return fmt.Errorf("%q is not a JWT", name)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct{ Exp int64 }
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return fmt.Errorf("no exp in %s: %v", payload, err)
	}
	// The server refuses a token from its exp second on; the margin covers
	// the CLI's start-up racing the boundary.
	time.Sleep(time.Until(time.Unix(claims.Exp, 0)) + 500*time.Millisecond)
	return nil
}

// setJFEnv sets a variable in the JFrog CLI's environment for the rest of
// the scenario, its value expanded (expand).
func (w *world) setJFEnv(name, value string) error {
	w.jfEnv = append(w.jfEnv, name+"="+w.expand(value))
	return nil
}

// runJF runs the JFrog CLI with its arguments expanded (expand).
// JFROG_CLI_HOME_DIR isolates the CLI's config from the developer's, and
// JFROG_CLI_TEMP_DIR its build-info between commands; both last for the
// scenario.
func (w *world) runJF(args string) error { return w.runJFIn(args, "") }

// runJFIn runs the JFrog CLI in a directory of the scenario's files, for
// commands that read the project there: jf go-config writes its
// .jfrog/projects/go.yaml where it runs, and jf go-publish reads that and the
// go.mod beside it.
func (w *world) runJFIn(args, sub string) error {
	if w.jfHome == "" {
		home, err := os.MkdirTemp("", "jfrog-home-")
		if err != nil {
			return err
		}
		w.t.Cleanup(func() { _ = os.RemoveAll(home) })
		if err := os.Mkdir(filepath.Join(home, "tmp"), 0o700); err != nil {
			return err
		}
		w.jfHome = home
	}
	home := w.jfHome
	argv := strings.Fields(w.expand(args))
	// Arguments split on whitespace, so "{sp}" puts a space inside one.
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "{sp}", " ")
	}
	cmd := exec.Command(os.Getenv("ARTIFACTWEAVE_JF_BIN"), argv...)
	// Relative file specs resolve against the scenario's files, so an
	// upload of "org/*" keeps the tree's layout under the target.
	cmd.Dir = w.dir
	if sub != "" {
		cmd.Dir = filepath.Join(w.dir, sub)
	}
	cmd.Env = append(
		os.Environ(),
		"JFROG_CLI_HOME_DIR="+home,
		// Unset, parallel runs on one machine share $TMPDIR/jfrog-<user>/builds.
		"JFROG_CLI_TEMP_DIR="+filepath.Join(home, "tmp"),
		"JFROG_CLI_REPORT_USAGE=false",
		// The CLI's own update check calls GitHub; tests stay offline.
		"JFROG_CLI_AVOID_NEW_VERSION_WARNING=true",
		"CI=true",
	)
	if w.dockerConfig != "" {
		// jf docker push and pull run the docker client, which reads the
		// scenario's docker login from here.
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+w.dockerConfig)
	}
	cmd.Env = append(cmd.Env, w.jfEnv...)
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

func (w *world) commandFails() error {
	if w.lastRC == 0 {
		return fmt.Errorf("the command succeeded; output:\n%s", w.body)
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

// keepHeaderPath keeps the path and query of a URL the last response
// carried in a header, such as a registry upload's Location, for {name}
// in a later request's path.
func (w *world) keepHeaderPath(header, name string) error {
	u, err := url.Parse(w.header.Get(header))
	if err != nil || u.Path == "" {
		return fmt.Errorf("header %s is no URL: %q", header, w.header.Get(header))
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars[name] = u.RequestURI()
	return nil
}

// keepOutputField keeps a string field of the JSON object in the last
// command's output (which may surround it with log lines) or the last
// response's body.
func (w *world) keepOutputField(field, name string) error {
	out := string(w.body)
	// Log lines can carry JSON of their own, so try each object in turn.
	for i := strings.Index(out, "{"); i >= 0; {
		var obj map[string]any
		if json.NewDecoder(strings.NewReader(out[i:])).Decode(&obj) == nil {
			if v, ok := obj[field].(string); ok && v != "" {
				if w.vars == nil {
					w.vars = map[string]string{}
				}
				w.vars[name] = v
				return nil
			}
		}
		next := strings.Index(out[i+1:], "{")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return fmt.Errorf("no JSON object with a string field %q in the output:\n%s", field, out)
}

// startIssuer starts a fake OIDC provider, a CI system's stand-in, whose
// URL is {issuer}.
func (w *world) startIssuer() error {
	i, err := idtokentest.New()
	if err != nil {
		return err
	}
	w.issuer = i
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["issuer"] = i.URL()
	return nil
}

// mintIDToken has the provider issue an ID token for audience, valid for
// an hour, with the document's claims, and keeps it as name.
func (w *world) mintIDToken(audience, name string, body *godog.DocString) error {
	if w.issuer == nil {
		return errors.New("no OIDC provider is running")
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(body.Content), &claims); err != nil {
		return err
	}
	raw, err := w.issuer.Mint(audience, time.Hour, claims)
	if err != nil {
		return err
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars[name] = raw
	return nil
}
