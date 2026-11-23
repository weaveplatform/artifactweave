package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// nginxImage renders the generated snippet. It is pinned by digest so
// the syntax check does not move under the test.
const nginxImage = "nginx:1.29.3-alpine@sha256:b3c656d55d7ad751196f21b7fd2e8d4da9cb430e32f646adcf92441b72f82b14"

// webServerSite is one OpenRuntime behind the router serve() builds, with
// the configured base URL and the configuration module.
type webServerSite struct {
	t      *testing.T
	server *httptest.Server
	rt     *Runtime
	admin  string
}

func openWebServerSite(t *testing.T, cfg config.Config) *webServerSite {
	t.Helper()
	rt, err := OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewRouter(RouterConfig{
		Store:                rt.Store,
		Blobs:                rt.Blobs,
		Auth:                 rt.Auth,
		Tokens:               rt.Tokens,
		Access:               rt.modules.access.API(),
		Configuration:        rt.modules.configuration.API(),
		Logging:              rt.Logging,
		BaseURL:              cfg.BaseURL,
		DockerSubdomainMatch: cfg.Docker.SubdomainMatch,
	}))
	s := &webServerSite{t: t, server: server, rt: rt, admin: cfg.Bootstrap.AdminPassword}
	t.Cleanup(s.close)
	return s
}

func (s *webServerSite) close() {
	if s.server == nil {
		return
	}
	s.server.Close()
	if err := s.rt.Close(context.Background()); err != nil {
		s.t.Error(err)
	}
	s.server = nil
}

// call sends body as user (admin with the bootstrap password, "" for no
// credentials) and returns the status and body.
func (s *webServerSite) call(method, path, user, body string, header ...string) (int, string) {
	s.t.Helper()
	return callAt(s.t, s.server.Client(), s.server.URL, method, path, user, s.admin, body, header...)
}

func callAt(
	t *testing.T, client *http.Client, base, method, path, user, password, body string, header ...string,
) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	switch user {
	case "":
	case "admin":
		req.SetBasicAuth(user, password)
	default:
		req.SetBasicAuth(user, webServerUserPassword)
	}
	if strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Content-Type", "text/plain")
	}
	for i := 0; i+1 < len(header); i += 2 {
		if header[i] == "Host" {
			req.Host = header[i+1]
			continue
		}
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func (s *webServerSite) must(method, path, user, body string, want int, header ...string) string {
	s.t.Helper()
	got, data := s.call(method, path, user, body, header...)
	if got != want {
		s.t.Fatalf("%s %s as %q = %d, want %d: %s", method, path, user, got, want, data)
	}
	return data
}

const (
	webServerPath         = "/artifactory/api/system/configuration/webServer"
	webServerSnippetPath  = "/artifactory/api/system/configuration/reverseProxy/nginx"
	webServerBaseURLPath  = "/artifactory/api/system/configuration/baseUrl"
	webServerUserPassword = "Reader-fixture-password-1"
)

// providerReverseProxy is what platform_reverse_proxy v2.2.11 sends for an
// NGINX proxy with the subdomain method: httpsPort, never sslPort, and no
// sslKey or sslCertificate without HTTPS.
const providerReverseProxy = `{"key":"nginx","webServerType":"NGINX","artifactoryAppContext":"artifactory",` +
	`"publicAppContext":"artifactory","serverName":"reg.example.test","artifactoryServerName":"localhost",` +
	`"dockerReverseProxyMethod":"SUBDOMAIN","useHttp":true,"useHttps":false,"httpPort":80,"httpsPort":443}`

// providerRead is the fields the resource reads back. Each must equal what
// it sent, or the next plan shows drift.
func providerRead(t *testing.T, body string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for _, k := range []string{
		"webServerType", "dockerReverseProxyMethod", "serverName", "artifactoryServerName",
		"useHttps", "httpPort", "httpsPort",
	} {
		out[k] = got[k]
	}
	return out
}

// TestAccWebServerSettings: PUT baseUrl and POST webServer persist through
// the descriptor service; reads, the XML export, the snippet and links agree
// before and after a restart; refused writes change nothing; and the
// disclosure boundary holds for administrators, users and anonymous callers.
func TestAccWebServerSettings(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Bootstrap.AdminPassword = "web-server-fixture-password"
			cfg.BaseURL = "https://install.example.test"
			if driver == "postgres" {
				cfg.Database.Driver = driver
				cfg.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
			}
			site := openWebServerSite(t, cfg)
			site.must("PUT", "/artifactory/api/repositories/ws-local", "admin", `{"rclass":"local","packageType":"generic"}`, 200)
			site.must("PUT", "/artifactory/ws-local/a.txt", "admin", "a", 201)
			site.must("PUT", "/artifactory/api/security/users/reader", "admin",
				`{"email":"reader@example.test","password":"`+webServerUserPassword+`"}`, 201)
			uri := func(s *webServerSite) string {
				t.Helper()
				var info struct {
					URI string `json:"uri"`
				}
				if err := json.Unmarshal([]byte(s.must("GET", "/artifactory/api/storage/ws-local/a.txt", "admin", "", 200)), &info); err != nil {
					t.Fatal(err)
				}
				return info.URI
			}
			if got := uri(site); got != "https://install.example.test/artifactory/api/storage/ws-local/a.txt" {
				t.Fatalf("base_url link %q", got)
			}
			// Without stored settings the read describes this server.
			if got := site.must("GET", webServerPath, "admin", "", 200); !strings.Contains(got, `"serverName":"install.example.test"`) {
				t.Fatalf("derived settings %s", got)
			}
			if got := site.must("PUT", webServerBaseURLPath, "admin", "https://reg.example.test/root", 200); got !=
				`URL base has been successfully updated to "https://reg.example.test/root".` {
				t.Fatalf("PUT baseUrl answer %q", got)
			}
			if got := uri(site); got != "https://reg.example.test/root/artifactory/api/storage/ws-local/a.txt" {
				t.Fatalf("urlBase link %q", got)
			}
			// The override header still wins over the configured base.
			_, body := site.call("GET", "/artifactory/api/storage/ws-local/a.txt", "admin", "",
				"X-JFrog-Override-Base-Url", "http://override.example.test")
			if !strings.Contains(body, `"uri":"http://override.example.test/artifactory/api/storage/ws-local/a.txt"`) {
				t.Fatalf("override %s", body)
			}
			site.must("POST", webServerPath, "admin", providerReverseProxy, 200)
			sent := providerRead(t, providerReverseProxy)

			// Refused writes change nothing.
			for _, bad := range []string{
				`{"webServerType":"NGINX","publicAppContext":"artifactory","serverName":"a.test","artifactoryServerName":"localhost","httpPort":0}`,
				`{"webServerType":"NGINX","publicAppContext":"artifactory","serverName":"a.test","artifactoryServerName":"localhost","dockerReverseProxyMethod":"PORTPERREPO"}`,
				`{"webServerType":"NGINX","publicAppContext":"artifactory","serverName":"a.test; return 200","artifactoryServerName":"localhost"}`,
				`{"webServerType":"NGINX","publicAppContext":"artifactory","serverName":"a.test","artifactoryServerName":"localhost","sslPort":443,"httpsPort":8443}`,
				`{"webServerType":"NGINX","publicAppContext":"artifactory","serverName":"a.test","artifactoryServerName":"localhost","useHttps":true}`,
				`{"webServerType":"HAPROXY","publicAppContext":"artifactory"}`,
				`{"webServerType":"NGINX","publicAppContext":"artifactory","httpPort":"80"}`,
			} {
				site.must("POST", webServerPath, "admin", bad, 400)
			}
			for _, bad := range []string{"ftp://reg.example.test", "https://u:p@reg.example.test", "not a url"} {
				site.must("PUT", webServerBaseURLPath, "admin", bad, 400)
			}
			site.must("POST", webServerPath, "reader", providerReverseProxy, 403)
			site.must("POST", webServerPath, "", providerReverseProxy, 401)
			site.must("PUT", webServerBaseURLPath, "reader", "https://reader.example.test", 403)
			site.must("GET", webServerSnippetPath, "reader", "", 403)

			state := func(s *webServerSite) []string {
				t.Helper()
				admin := s.must("GET", webServerPath, "admin", "", 200)
				if got := providerRead(t, admin); !mapsEqual(got, sent) {
					t.Fatalf("provider read %v, sent %v", got, sent)
				}
				if !strings.Contains(admin, `"sslPort":443`) || !strings.Contains(admin, `"sslKey":""`) {
					t.Fatalf("admin read %s", admin)
				}
				user := s.must("GET", webServerPath, "reader", "", 200)
				for _, hidden := range []string{"serverName", "ssl" + "Key", "sslCertificate", "artifactoryServerName", "AppContext", "key\""} {
					if strings.Contains(user, hidden) {
						t.Fatalf("user read discloses %s: %s", hidden, user)
					}
				}
				if !strings.Contains(user, `"dockerReverseProxyMethod":"SUBDOMAIN"`) || !strings.Contains(user, `"httpsPort":443`) {
					t.Fatalf("user read %s", user)
				}
				s.must("GET", webServerPath, "", "", 401)
				xml := s.must("GET", "/artifactory/api/system/configuration", "admin", "", 200)
				for _, want := range []string{
					"<urlBase>https://reg.example.test/root</urlBase>",
					"<webServerType>nginx</webServerType>",
					"<dockerReverseProxyMethod>subDomain</dockerReverseProxyMethod>",
					"<serverName>reg.example.test</serverName>",
				} {
					if !strings.Contains(xml, want) {
						t.Fatalf("export lacks %s", want)
					}
				}
				snippet := s.must("GET", webServerSnippetPath, "admin", "", 200)
				return []string{admin, user, snippet, uri(s)}
			}
			before := state(site)
			site.close()
			after := state(openWebServerSite(t, cfg))
			for i := range before {
				if before[i] != after[i] {
					t.Errorf("restart changed answer %d:\n%s\n%s", i, before[i], after[i])
				}
			}
		})
	}
}

func mapsEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// TestAccWebServerNginxSnippet loads the generated snippet into a pinned
// nginx, which refuses to start on a configuration nginx -t rejects, and
// sends representative requests through it over HTTP and HTTPS: a ping, a
// link built on the X-JFrog-Override-Base-Url header the snippet sets, and
// a Docker request that the subdomain rewrite routes to its repository.
func TestAccWebServerNginxSnippet(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "nginx-fixture-password"
	site := openWebServerSite(t, cfg)
	site.must("PUT", "/artifactory/api/repositories/ws-local", "admin", `{"rclass":"local","packageType":"generic"}`, 200)
	site.must("PUT", "/artifactory/ws-local/a.txt", "admin", "a", 201)
	site.must("PUT", "/artifactory/api/repositories/docker-local", "admin", `{"rclass":"local","packageType":"docker"}`, 200)
	served, err := url.Parse(site.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(served.Port())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := json.Marshal(map[string]any{
		"key": "nginx", "webServerType": "NGINX", "artifactoryAppContext": "artifactory",
		"publicAppContext": "artifactory", "serverName": "reg.example.test",
		"artifactoryServerName": testcontainers.HostInternal, "artifactoryPort": port,
		"dockerReverseProxyMethod": "SUBDOMAIN", "useHttp": true, "useHttps": true,
		"httpPort": 80, "httpsPort": 443,
		"sslCertificate": "/etc/nginx/certs/reg.pem", "sslKey": "/etc/nginx/certs/reg.key",
	})
	if err != nil {
		t.Fatal(err)
	}
	site.must("POST", webServerPath, "admin", string(proxy), 200)
	snippet := site.must("GET", webServerSnippetPath, "admin", "", 200)
	certPEM, keyPEM := selfSigned(t, "reg.example.test")
	ctx := t.Context()
	nginx, err := testcontainers.Run(ctx, nginxImage,
		testcontainers.WithExposedPorts("80/tcp", "443/tcp"),
		testcontainers.WithHostPortAccess(port),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(snippet), ContainerFilePath: "/etc/nginx/conf.d/default.conf", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: bytes.NewReader(certPEM), ContainerFilePath: "/etc/nginx/certs/reg.pem", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: bytes.NewReader(keyPEM), ContainerFilePath: "/etc/nginx/certs/reg.key", FileMode: 0o600},
		),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("80/tcp").WithStartupTimeout(time.Minute)),
	)
	testcontainers.CleanupContainer(t, nginx)
	if err != nil {
		t.Fatalf("nginx did not start on the snippet: %v\n%s", err, snippet)
	}
	if code, out, err := nginx.Exec(ctx, []string{"nginx", "-t"}); err != nil || code != 0 {
		text, _ := io.ReadAll(out)
		t.Fatalf("nginx -t = %d %v: %s\n%s", code, err, text, snippet)
	}
	endpoint := func(port string, scheme string) string {
		t.Helper()
		e, err := nginx.PortEndpoint(ctx, port, scheme)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	plain := endpoint("80/tcp", "http")
	secure := endpoint("443/tcp", "https")
	insecure := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "reg.example.test"}, //nolint:gosec // the fixture's own self-signed certificate
	}}
	for _, base := range []struct {
		client *http.Client
		url    string
		port   string
		scheme string
	}{{http.DefaultClient, plain, "80", "http"}, {insecure, secure, "443", "https"}} {
		code, body := callAt(t, base.client, base.url, "GET", "/artifactory/api/system/ping", "", "", "", "Host", "reg.example.test")
		if code != 200 || body != "OK" {
			t.Fatalf("%s ping = %d %q", base.scheme, code, body)
		}
		// Links name the proxy's host and port, from the header the
		// snippet sets, not the upstream artifactweave was reached on.
		code, body = callAt(t, base.client, base.url, "GET", "/artifactory/api/storage/ws-local/a.txt", "admin",
			cfg.Bootstrap.AdminPassword, "", "Host", "reg.example.test")
		want := `"uri":"` + base.scheme + "://reg.example.test:" + base.port + `/artifactory/api/storage/ws-local/a.txt"`
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("%s storage = %d %s; want %s", base.scheme, code, body, want)
		}
		// docker-local.reg.example.test/v2/ is rewritten to the repository.
		code, body = callAt(t, base.client, base.url, "GET", "/v2/", "admin", cfg.Bootstrap.AdminPassword, "",
			"Host", "docker-local.reg.example.test")
		if code != 200 {
			t.Fatalf("%s docker subdomain = %d %s", base.scheme, code, body)
		}
	}
}

// selfSigned is a certificate and key for host, valid for an hour.
func selfSigned(t *testing.T, host string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host, "*." + host},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
