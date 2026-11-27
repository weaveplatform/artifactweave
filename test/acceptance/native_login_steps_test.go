package acceptance

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cucumber/godog"
	"github.com/go-chi/chi/v5"
)

type nativeURLs struct {
	LoginURL string `json:"loginUrl"`
	DoneURL  string `json:"doneUrl"`
}
type nativeLoginFixture struct {
	enabled     bool
	front       *httptest.Server
	client      *http.Client
	urls        chan nativeURLs
	pending     chan struct{}
	token       string
	callback    func(context.Context, string) error
	exchanges   atomic.Int64
	startStatus atomic.Int64
	npmTokens   atomic.Int64
}

func (w *world) registerNativeLogin(sc *godog.ScenarioContext) {
	sc.Step(`^the registry supports native browser approval$`, w.startNativeLogin)
	sc.Step(`^the registry has native browser approval disabled$`, func() error { return w.startNativeLoginMode(false) })
	sc.Step(`^(npm|npm 12) completes browser approval and uses its stored credential$`, w.nativeNPMLogin)
	sc.Step(`^Terraform completes browser approval and initializes a protected backend$`, w.nativeTerraformLogin)
	sc.Step(`^the native approval token preserves permissions and is revocable$`, w.nativeTokenPermissions)
	sc.Step(`^(npm|npm 12) is refused browser credentials while approval is disabled$`, w.nativeNPMDisabled)
}

// The TLS origin exists before the application starts, so canonical login URLs
// need neither an insecure forwarded-header override nor a configuration change
// after a client has already started. The proxy records protocol boundaries only.
func (w *world) startNativeLogin() error {
	return w.startNativeLoginMode(true)
}

func (w *world) startNativeLoginMode(enabled bool) error {
	executable, err := os.Open(binary)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, executable)
	closeErr := executable.Close()
	if hashErr != nil {
		return hashErr
	}
	if closeErr != nil {
		return closeErr
	}
	w.t.Logf("native approval server binary SHA256 %x", hash.Sum(nil))

	f := &nativeLoginFixture{enabled: enabled, urls: make(chan nativeURLs, 1), pending: make(chan struct{}, 1)}
	w.nativeLogin = f
	var target atomic.Pointer[url.URL]
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) {
		u := target.Load()
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		r.Host = u.Host
	}}
	proxy.ModifyResponse = func(r *http.Response) error {
		if strings.HasSuffix(r.Request.URL.Path, "/-/v1/login") {
			f.startStatus.Store(int64(r.StatusCode))
		}
		if strings.HasSuffix(r.Request.URL.Path, "/-/v1/done") && r.StatusCode == 200 {
			f.npmTokens.Add(1)
		}
		if strings.HasSuffix(r.Request.URL.Path, "/-/v1/login") && r.StatusCode == 200 {
			body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
			_ = r.Body.Close()
			if err != nil {
				return err
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var urls nativeURLs
			if err := json.Unmarshal(body, &urls); err != nil {
				return err
			}
			select {
			case f.urls <- urls:
			default:
			}
		}
		if strings.HasSuffix(r.Request.URL.Path, "/-/v1/done") && r.StatusCode == 202 {
			select {
			case f.pending <- struct{}{}:
			default:
			}
		}
		if r.Request.URL.Path == "/artifactory/api/oauth2/token" && r.StatusCode == 200 {
			f.exchanges.Add(1)
		}
		return nil
	}
	r := chi.NewRouter()
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target.Load() == nil {
			http.Error(w, "fixture starting", 503)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	f.front, err = nativeTLSFront(r)
	if err != nil {
		return err
	}
	w.t.Cleanup(f.front.Close)
	if err := w.start(); err != nil {
		return err
	}
	u, err := url.Parse(w.url)
	if err != nil {
		return err
	}
	target.Store(u)
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	f.client = f.front.Client()
	f.client.Jar = jar
	f.client.Timeout = 15 * time.Second
	transport := f.client.Transport.(*http.Transport).Clone()
	public, _ := url.Parse(f.front.URL)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == public.Host {
			address = net.JoinHostPort("127.0.0.1", public.Port())
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	f.client.Transport = transport
	f.client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Hostname() == "localhost" {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 {
			return fmt.Errorf("native fixture redirect loop")
		}
		return nil
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.front.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "native-ca.pem"), cert, 0o600); err != nil {
		return err
	}
	for _, entry := range []struct{ path, body string }{
		{"/artifactory/api/repositories/native-login", `{"rclass":"local","packageType":"npm"}`},
		{"/artifactory/api/repositories/native-secret", `{"rclass":"local","packageType":"generic"}`},
		{"/artifactory/api/security/users/native-user", `{"name":"native-user","email":"native@example.test","password":"Native-Password-1"}`},
	} {
		if err := w.backendRequest("PUT", entry.path, entry.body, 200); err != nil {
			// New legacy users answer201; repository writes answer200.
			if entry.path != "/artifactory/api/security/users/native-user" || w.status != 201 {
				return err
			}
		}
	}
	if err := w.backendRequest("PATCH", "/access/api/v2/groups/readers/members", `{"remove":["native-user"]}`, 200); err != nil {
		return err
	}
	if err := w.backendRequest(
		"POST",
		"/access/api/v2/permissions",
		`{"name":"native-login-read","resources":{"artifact":{"actions":{"users":{"native-user":["READ"]}},"targets":{"native-login":{"include_patterns":["**"]}}}}}`,
		201,
	); err != nil {
		return err
	}
	for _, path := range []string{"/artifactory/native-login/allowed.txt", "/artifactory/native-secret/secret.txt"} {
		if err := w.backendRequest("PUT", path, "protected bytes", 201); err != nil {
			return err
		}
	}
	return nil
}

var approvalCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (f *nativeLoginFixture) approve(ctx context.Context, loginURL string) error {
	csrf := func(resp *http.Response) (string, error) {
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if err != nil {
			return "", err
		}
		match := approvalCSRF.FindSubmatch(body)
		if resp.StatusCode != 200 || len(match) != 2 {
			return "", fmt.Errorf("native form status=%d, missing CSRF", resp.StatusCode)
		}
		if strings.Contains(string(body), "<script") {
			return "", fmt.Errorf("approval requires JavaScript")
		}
		return string(match[1]), nil
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(r)
	if err != nil {
		return err
	}
	token, err := csrf(resp)
	if err != nil {
		return err
	}
	post := func(values url.Values) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, resp.Request.URL.String(), strings.NewReader(values.Encode()))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", f.front.URL)
		return f.client.Do(r)
	}
	resp, err = post(url.Values{"action": {"login"}, "csrf": {token}, "username": {"native-user"}, "password": {"Native-Password-1"}})
	if err != nil {
		return err
	}
	token, err = csrf(resp)
	if err != nil {
		return err
	}
	resp, err = post(url.Values{"action": {"approve"}, "csrf": {token}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 303 && f.callback != nil {
		return f.callback(ctx, resp.Header.Get("Location"))
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("native approval/callback status=%d", resp.StatusCode)
	}
	return nil
}

func (w *world) nativeDocker(ctx context.Context, name string, extra, args []string) *exec.Cmd {
	flags := []string{
		"run",
		"--rm",
		"--name",
		name,
		"--label",
		"io.weaveplatform.task=262",
		"--label",
		"io.weaveplatform.owner=native-browser-login",
		"--network",
		"host",
		"-v",
		w.dir + ":/work",
		"-w",
		"/work",
		"-e",
		"NODE_EXTRA_CA_CERTS=/work/native-ca.pem",
	}
	flags = append(flags, extra...)
	flags = append(flags, w.npmImage)
	flags = append(flags, args...)
	return exec.CommandContext(ctx, "docker", flags...)
}

func (w *world) nativeNPMLogin(client string) error {
	f := w.nativeLogin
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("artifactweave-262-npm-%d", time.Now().UnixNano())
	extra := []string{
		"--user",
		fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--entrypoint",
		"npm",
		"-e",
		"npm_config_userconfig=/work/native.npmrc",
		"-e",
		"npm_config_cache=/work/native-npm-cache",
		"-e",
		"npm_config_update_notifier=false",
	}
	registry := f.front.URL + "/artifactory/api/npm/native-login/"
	version := w.nativeDocker(ctx, name+"-version", extra, []string{"version", "--json"})
	versions, err := version.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pinned npm version probe: %w: %s", err, versions)
	}
	var actual map[string]string
	if err := json.Unmarshal(versions, &actual); err != nil {
		return err
	}
	wantNPM, wantNode := "11.20.0", "26.13.1"
	if client == "npm 12" {
		wantNPM, wantNode = "12.2.1", "24.22.3"
	}
	if actual["npm"] != wantNPM || actual["node"] != wantNode {
		return fmt.Errorf("client versions npm=%s node=%s want=%s/%s", actual["npm"], actual["node"], wantNPM, wantNode)
	}
	w.t.Logf("native approval client npm=%s node=%s", actual["npm"], actual["node"])
	cmd := w.nativeDocker(ctx, name, extra, []string{"login", "--auth-type=web", "--browser=false", "--fetch-retries=0", "--fetch-timeout=15000", "--registry", registry})
	var output syncBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	defer func() {
		if !finished {
			cancel()
			_ = exec.Command("docker", "rm", "--force", name).Run()
			<-done
		}
	}()
	var urls nativeURLs
	select {
	case urls = <-f.urls:
	case err := <-done:
		finished = true
		return fmt.Errorf("npm exited before web bootstrap: %w: %s", err, output.String())
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-f.pending:
	case err := <-done:
		finished = true
		return fmt.Errorf("npm exited before pending poll: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := f.approve(ctx, urls.LoginURL); err != nil {
		return err
	}
	select {
	case err := <-done:
		finished = true
		if err != nil {
			return fmt.Errorf("native npm login: %w: %s", err, output.String())
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	data, err := os.ReadFile(filepath.Join(w.dir, "native.npmrc"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, value, ok := strings.Cut(line, ":_authToken="); ok {
			f.token = value
		}
	}
	if f.token == "" {
		return fmt.Errorf("npm did not persist the web credential")
	}
	check := w.nativeDocker(ctx, name+"-whoami", extra, []string{"whoami", "--registry", registry})
	result, err := check.CombinedOutput()
	if err != nil || strings.TrimSpace(string(result)) != "native-user" {
		return fmt.Errorf("npm stored credential unusable: %w: %s", err, result)
	}
	return nil
}

// Docker Desktop cannot reach the host's127.0.0.1 from a container. The test's
// public HTTPS name has an explicit certificate SAN; the host form driver dials
// that same listener directly without weakening TLS verification or product origin.
func nativeTLSFront(h http.Handler) (*httptest.Server, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	description := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "native approval fixture"},
		DNSNames:              []string{"host.docker.internal", "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, description, description, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	s := httptest.NewUnstartedServer(h)
	_ = s.Listener.Close()
	s.Listener = listener
	s.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.StartTLS()
	host := "127.0.0.1"
	if runtime.GOOS == "darwin" {
		host = "host.docker.internal"
	}
	s.URL = "https://" + net.JoinHostPort(host, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	return s, nil
}

func (w *world) nativeNPMDisabled(_ string) error {
	f := w.nativeLogin
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := fmt.Sprintf("artifactweave-262-off-%d", time.Now().UnixNano())
	extra := []string{
		"--user",
		fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--entrypoint",
		"npm",
		"-e",
		"npm_config_userconfig=/work/native.npmrc",
		"-e",
		"npm_config_cache=/work/native-npm-cache",
		"-e",
		"npm_config_update_notifier=false",
	}
	cmd := w.nativeDocker(
		ctx,
		name,
		extra,
		[]string{"login", "--auth-type=web", "--browser=false", "--fetch-retries=0", "--fetch-timeout=10000", "--registry", f.front.URL + "/artifactory/api/npm/native-login/"},
	)
	_, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		_ = exec.Command("docker", "rm", "--force", name).Run()
		return fmt.Errorf("disabled npm fallback exceeded deadline: %w", ctx.Err())
	}
	if err == nil || f.startStatus.Load() != 401 || f.npmTokens.Load() != 0 {
		return fmt.Errorf("disabled npm web flow: exit=%v start=%d deliveries=%d", err, f.startStatus.Load(), f.npmTokens.Load())
	}
	data, readErr := os.ReadFile(filepath.Join(w.dir, "native.npmrc"))
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if strings.Contains(string(data), "_authToken") {
		return fmt.Errorf("disabled web flow stored a credential")
	}
	resp, err := f.client.Get(f.front.URL + "/.well-known/terraform.json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var services map[string]json.RawMessage
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&services) != nil || services["login.v1"] != nil || services["tfe.v2"] == nil {
		return fmt.Errorf("disabled discovery lost pasted-token fallback or advertised approval")
	}
	return nil
}

func (w *world) nativeTokenPermissions() error {
	f := w.nativeLogin
	for _, tc := range []struct {
		path   string
		status int
	}{{"/artifactory/native-login/allowed.txt", 200}, {"/artifactory/native-secret/secret.txt", 403}} {
		r, err := http.NewRequestWithContext(w.t.Context(), http.MethodGet, f.front.URL+tc.path, nil)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+f.token)
		resp, err := f.client.Do(r)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode != tc.status || tc.status == 200 && string(body) != "protected bytes" {
			return fmt.Errorf("native credential permission status=%d want=%d", resp.StatusCode, tc.status)
		}
	}
	r, err := http.NewRequestWithContext(w.t.Context(), http.MethodDelete, f.front.URL+"/artifactory/api/npm/native-login/-/user/token/"+url.PathEscape(f.token), nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(r)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("native token logout=%d", resp.StatusCode)
	}
	r, err = http.NewRequestWithContext(w.t.Context(), http.MethodGet, f.front.URL+"/artifactory/native-login/allowed.txt", nil)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+f.token)
	resp, err = f.client.Do(r)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		return fmt.Errorf("revoked native token still works: %d", resp.StatusCode)
	}
	return nil
}

var terraformAuthorization = regexp.MustCompile(`https://[^\s]+/ui/terraform/oauth2/authorize\?[^\s]+`)

// Terraform runs in a disposable pinned Node client container with its default
// HOME. All host configuration paths are explicit; no user's Terraform files or
// HOME variable are changed. Its real callback is driven in that same network
// namespace, including on Docker Desktop where host/container loopbacks differ.
func (w *world) nativeTerraformLogin() error {
	f := w.nativeLogin
	bin := os.Getenv("ARTIFACTWEAVE_TERRAFORM_BIN")
	if runtime.GOOS != "linux" {
		bin = os.Getenv("ARTIFACTWEAVE_TERRAFORM_LOGIN_LINUX_BIN")
	}
	if bin == "" {
		return fmt.Errorf("native login needs the pinned Linux Terraform executable for its disposable client container")
	}
	if err := w.backendRequest("PUT", "/artifactory/api/repositories/native-state", `{"rclass":"local","packageType":"terraformbackend"}`, 200); err != nil {
		return err
	}
	if err := w.backendRequest(
		"POST",
		"/access/api/v2/permissions",
		`{"name":"native-state","resources":{"artifact":{"actions":{"users":{"native-user":["READ","WRITE","DELETE"]}},"targets":{"native-state":{"include_patterns":["**"]}}}}}`,
		201,
	); err != nil {
		return err
	}
	host := strings.TrimPrefix(f.front.URL, "https://")
	configuration := fmt.Sprintf("terraform {\n backend \"remote\" {\n hostname = %q\n organization = \"native-state\"\n workspaces { name = \"native\" }\n }\n}\n", host)
	for name, content := range map[string]string{"native.tf": configuration, "terraform-native.rc": "", "native-callback.js": `let input='';process.stdin.on('data',b=>input+=b);process.stdin.on('end',async()=>{try{const r=await fetch(JSON.parse(input).url,{redirect:'manual'});process.stdout.write(String(r.status));await r.arrayBuffer()}catch(e){console.error('callback failed');process.exitCode=1}});`} {
		if err := os.WriteFile(filepath.Join(w.dir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("artifactweave-262-terraform-%d", time.Now().UnixNano())
	extra := []string{
		"-i",
		"--entrypoint",
		"sh",
		"-v",
		bin + ":/clients/terraform:ro",
		"-e",
		"TF_CLI_CONFIG_FILE=/work/terraform-native.rc",
		"-e",
		"TF_DATA_DIR=/tmp/native-tf-data",
		"-e",
		"CHECKPOINT_DISABLE=1",
		"-e",
		"SSL_CERT_FILE=/work/native-ca.pem",
		"-e",
		"BROWSER=true",
		"-e",
		"AW_NATIVE_HOST=" + host,
		"-e",
		fmt.Sprintf("AW_NATIVE_OWNER=%d:%d", os.Getuid(), os.Getgid()),
	}
	script := `set -eu
mkdir -p /root/.terraform.d
/clients/terraform version -json
/clients/terraform login -no-color "$AW_NATIVE_HOST"
cp /root/.terraform.d/credentials.tfrc.json /work/native-credentials.json
chown "$AW_NATIVE_OWNER" /work/native-credentials.json
chmod 600 /work/native-credentials.json
TF_CLI_CONFIG_FILE=/root/.terraform.d/credentials.tfrc.json /clients/terraform init -no-color -input=false
`
	cmd := w.nativeDocker(ctx, name, extra, []string{"-c", script})
	cmd.Stdin = strings.NewReader("yes\n")
	var output syncBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	defer func() {
		if !finished {
			cancel()
			_ = exec.Command("docker", "rm", "--force", name).Run()
			<-done
		}
	}()
	var authz string
	for authz == "" {
		authz = terraformAuthorization.FindString(output.String())
		if authz != "" {
			break
		}
		select {
		case err := <-done:
			finished = true
			return fmt.Errorf("Terraform exited before approval: %w: %s", err, output.String())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	u, err := url.Parse(authz)
	if err != nil {
		return err
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("state") == "" || q.Get("client_id") != "terraform-cli" {
		return fmt.Errorf("pinned Terraform omitted PKCE/state/client bindings")
	}
	callback := func(ctx context.Context, target string, want string) error {
		payload, err := json.Marshal(map[string]string{"url": target})
		if err != nil {
			return err
		}
		call := exec.CommandContext(ctx, "docker", "exec", "-i", name, "node", "/work/native-callback.js")
		call.Stdin = bytes.NewReader(payload)
		out, err := call.CombinedOutput()
		if err != nil || string(out) != want {
			return fmt.Errorf("actual Terraform callback=%s want=%s: %w", out, want, err)
		}
		return nil
	}
	// These are requests to the actual running CLI listener, not a fake OAuth
	// server. Neither malformed callback may produce a token exchange.
	if err := callback(ctx, q.Get("redirect_uri")+"?state=wrong&code=fake", "400"); err != nil {
		return err
	}
	if err := callback(ctx, q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state")), "400"); err != nil {
		return err
	}
	if f.exchanges.Load() != 0 {
		return fmt.Errorf("malformed callback exchanged a token")
	}
	f.callback = func(ctx context.Context, target string) error { return callback(ctx, target, "200") }
	if err := f.approve(ctx, authz); err != nil {
		return err
	}
	select {
	case err := <-done:
		finished = true
		if err != nil {
			return fmt.Errorf("native Terraform login/init: %w: %s", err, output.String())
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if !strings.Contains(output.String(), `"terraform_version": "1.17.1"`) || !strings.Contains(output.String(), "Terraform has been successfully initialized!") ||
		f.exchanges.Load() != 1 {
		return fmt.Errorf("pinned Terraform did not complete exactly one exchange and protected init: %s", output.String())
	}
	data, err := os.ReadFile(filepath.Join(w.dir, "native-credentials.json"))
	if err != nil {
		return err
	}
	var credentials struct {
		Credentials map[string]struct {
			Token string `json:"token"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(data, &credentials); err != nil {
		return err
	}
	f.token = credentials.Credentials[host].Token
	if f.token == "" {
		return fmt.Errorf("Terraform did not persist its native credential")
	}
	return nil
}
