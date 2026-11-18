package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerExternalNPM(sc *godog.ScenarioContext) {
	sc.Step(`^external npm tarball and GitHub dependencies are available$`, w.npmExternalFixtures)
	sc.Step(`^the external npm proxy is configured$`, w.npmExternalProxyConfig)
	sc.Step(`^the external npm parent is deployed to "([^"]*)"$`, w.npmExternalParent)
	sc.Step(`^I run isolated external npm "([^"]*)"$`, w.runExternalNPM)
	sc.Step(`^external npm origins received no additional requests$`, w.npmExternalWarm)
	sc.Step(`^external npm origin counts are recorded$`, w.npmExternalCounts)
	sc.Step(`^the external npm lockfile uses virtual archives only$`, w.npmExternalLock)
}

func npmExternalArchive(name, version string, fields map[string]any) ([]byte, error) {
	return npmExternalArchiveRoot("package", name, version, fields)
}

func npmExternalArchiveRoot(root, name, version string, fields map[string]any) ([]byte, error) {
	manifest := map[string]any{"name": name, "version": version}
	for k, v := range fields {
		manifest[k] = v
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{{root + "/package.json", raw}, {root + "/marker.txt", []byte(name + "-external-bytes")}} {
		if err = tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.data))}); err != nil {
			return nil, err
		}
		if _, err = tw.Write(entry.data); err != nil {
			return nil, err
		}
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (w *world) npmExternalFixtures() error {
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "cache-only", Password: "source-secret"}})
	w.closers = append(w.closers, w.remote.Close)
	w.vars["upstream"] = w.remote.URL
	tgz, err := npmExternalArchive("@acme/external-tgz", "1.0.0", nil)
	if err != nil {
		return err
	}
	w.remote.Set("/external.tgz", fakeupstream.Resource{Body: tgz})
	cert, key, ca, err := npmGitHubCertificate()
	if err != nil {
		return err
	}
	identity, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return err
	}
	github := fakeupstream.New(w.t, fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{identity}}})
	w.closers = append(w.closers, github.Close)
	archive, err := npmExternalArchiveRoot("dependency-2", "external-git", "2.0.0", nil)
	if err != nil {
		return err
	}
	github.Set("/fixture/dependency/archive/v2.tar.gz", fakeupstream.Resource{Body: archive})
	caFile := filepath.Join(w.t.TempDir(), "npm-external-ca.pem")
	if err = os.WriteFile(caFile, ca, 0o600); err != nil {
		return err
	}
	w.env = append(w.env, "SSL_CERT_FILE="+caFile)
	proxy := npmExternalProxy(w, github, w.remote)
	parsed, err := url.Parse(proxy.URL)
	if err != nil {
		return err
	}
	w.vars["external-proxy-host"] = parsed.Hostname()
	w.vars["external-proxy-port"] = parsed.Port()
	// Expose only counts, never a direct client route to the GitHub fixture.
	w.closers = append(w.closers, func() {
		if github.Count("GET", "/fixture/dependency/archive/v2.tar.gz") != 1 {
			w.t.Errorf("GitHub archive GET count=%d", github.Count("GET", "/fixture/dependency/archive/v2.tar.gz"))
		}
		for _, request := range github.Requests() {
			for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-External-Secret"} {
				if request.Header.Get(header) != "" {
					w.t.Errorf("cross-origin archive received %s", header)
				}
			}
		}
	})
	return nil
}

func npmGitHubCertificate() ([]byte, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	cert := &x509.Certificate{
		SerialNumber:          big.NewInt(1568),
		Subject:               pkix.Name{CommonName: "npm external fixture"},
		DNSNames:              []string{"github.com"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return ca, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), ca, nil
}

// The forward proxy can reach only these two running fixtures. CONNECT's
// physical DNS-pinned address is replaced by the local github.com TLS witness;
// the production client still validates GitHub's logical host/SNI and fixture CA.
func npmExternalProxy(w *world, github, tarballs *fakeupstream.Server) *httptest.Server {
	target, _ := url.Parse(tarballs.URL)
	gh, _ := url.Parse(github.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	router := chi.NewRouter()
	router.Handle("/*", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			if request.URL.Host != target.Host {
				response.WriteHeader(403)
				return
			}
			proxy.ServeHTTP(response, request)
			return
		}
		_, port, err := net.SplitHostPort(request.Host)
		if err != nil || port != "443" {
			response.WriteHeader(403)
			return
		}
		upstream, err := (&net.Dialer{}).DialContext(request.Context(), "tcp", gh.Host)
		if err != nil {
			response.WriteHeader(502)
			return
		}
		client, buffer, err := http.NewResponseController(response).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		mu.Lock()
		connections[client] = true
		connections[upstream] = true
		mu.Unlock()
		defer func() {
			_ = client.Close()
			_ = upstream.Close()
			mu.Lock()
			delete(connections, client)
			delete(connections, upstream)
			mu.Unlock()
		}()
		if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err = buffer.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); close(done) }()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		<-done
	}))
	server := httptest.NewServer(router)
	w.closers = append(w.closers, func() {
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		server.Close()
	})
	return server
}

func (w *world) npmExternalProxyConfig() error {
	body := fmt.Sprintf("proxies:\n  external-proxy:\n    host: %s\n    port: %s\n", w.vars["external-proxy-host"], w.vars["external-proxy-port"])
	return w.patchYAMLAsAdmin("/artifactory/api/system/configuration", &godog.DocString{Content: body})
}

func (w *world) npmExternalParent(key string) error {
	data, err := npmExternalArchive(
		"external-root",
		"1.0.0",
		map[string]any{"dependencies": map[string]string{"@acme/external-tgz": w.remote.URL + "/external.tgz", "external-git": "github:fixture/dependency#v2"}},
	)
	if err != nil {
		return err
	}
	return w.send("PUT", "/artifactory/"+key+"/external-root/-/external-root-1.0.0.tgz", string(data), func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

func (w *world) runExternalNPM(args string) error {
	return w.runContainer(
		w.npmImage,
		[]string{
			"--entrypoint",
			"npm",
			"--add-host",
			"github.com:127.0.0.1",
			"--add-host",
			"codeload.github.com:127.0.0.1",
			"-e",
			"npm_config_update_notifier=false",
			"-e",
			"npm_config_audit=false",
			"-e",
			"npm_config_fund=false",
		},
		w.clientArgs(args),
	)
}

func (w *world) npmExternalCounts() error {
	w.vars["external-tgz-count"] = strconv.Itoa(w.remote.Count("GET", "/external.tgz"))
	return nil
}

func (w *world) npmExternalWarm() error {
	count, _ := strconv.Atoi(w.vars["external-tgz-count"])
	if w.remote.Count("GET", "/external.tgz") != count {
		return fmt.Errorf("warm install contacted tarball origin")
	}
	for _, request := range w.remote.Requests() {
		if request.Path == "/external.tgz" && !request.Authorized {
			return fmt.Errorf("client attempted direct tarball download")
		}
	}
	return nil
}

func (w *world) npmExternalLock() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return err
	}
	var lock struct {
		Packages map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err = json.Unmarshal(raw, &lock); err != nil {
		return err
	}
	for _, name := range []string{"external-root", "@acme/external-tgz", "external-git"} {
		entry, ok := lock.Packages["node_modules/"+name]
		if !ok || !strings.HasPrefix(entry.Resolved, w.url+"/artifactory/api/npm/npm-external/") || entry.Integrity == "" {
			return fmt.Errorf("external lockfile%s: %+v", name, entry)
		}
	}
	return nil
}
