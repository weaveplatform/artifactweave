package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

var publishedP2Files = map[string]string{
	"p2.index":         "p2.index",
	"content.jar":      "content.jar",
	"artifacts.jar":    "artifacts.jar",
	"content.xml.xz":   "content.xml.xz",
	"artifacts.xml.xz": "artifacts.xml.xz",
	"plugins/org.eclipse.glsp.layout.source_2.7.0.jar": "org.eclipse.glsp.layout.source_2.7.0.jar",
}

func (w *world) registerP2(sc *godog.ScenarioContext) {
	sc.Step(`^the published Eclipse P2 update site is available upstream$`, w.p2Upstream)
	sc.Step(
		`^the published Eclipse P2 update site is available as an archive upstream$`,
		w.p2ArchiveUpstream,
	)
	sc.Step(`^the published Eclipse P2 update site is under "([^"]*)"$`, w.p2Files)
	sc.Step(`^the P2 repository is "([^"]*)"$`, w.p2Repository)
	sc.Step(`^I run the Eclipse P2 director "(list|install)"$`, w.runP2)
	sc.Step(`^I clear the Eclipse P2 installation$`, w.clearP2Install)
	sc.Step(`^the installed P2 bundle matches the published SHA256$`, w.p2InstalledHash)
}

func (w *world) p2InstalledHash() error {
	base, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(
		filepath.Join(base, "p2-install", "plugins", "org.eclipse.glsp.layout.source_2.7.0.jar"),
	)
	if err != nil {
		return err
	}
	const published = "80c9be399f166cddc9934a42e27484e6427684bf8adf398fbfeb8224a189a728"
	if actual := fmt.Sprintf("%x", sha256.Sum256(body)); actual != published {
		return fmt.Errorf("installed Eclipse bundle SHA256 %s, want %s", actual, published)
	}
	return nil
}

func (w *world) p2ArchiveUpstream() error {
	if w.remote == nil {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
		w.closers = append(w.closers, w.remote.Close)
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["upstream"] = w.remote.URL
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for path, file := range publishedP2Files {
		body, err := p2PublishedFile(file)
		if err != nil {
			return err
		}
		entry, err := zw.Create(path)
		if err != nil {
			return err
		}
		if _, err := entry.Write(body); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	w.remote.Set("/site.zip", fakeupstream.Resource{Body: archive.Bytes()})
	return nil
}

func p2PublishedFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "internal", "format", "p2", "testdata", name))
}

func (w *world) p2Upstream() error {
	if w.remote == nil {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
		w.closers = append(w.closers, w.remote.Close)
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["upstream"] = w.remote.URL
	}
	for path, file := range publishedP2Files {
		body, err := p2PublishedFile(file)
		if err != nil {
			return err
		}
		w.remote.Set("/"+path, fakeupstream.Resource{Body: body})
	}
	return nil
}

func (w *world) p2Files(folder string) error {
	base, err := w.filesDir()
	if err != nil {
		return err
	}
	for path, file := range publishedP2Files {
		body, err := p2PublishedFile(file)
		if err != nil {
			return err
		}
		dest := filepath.Join(base, folder, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dest, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) p2Repository(key string) error {
	if w.p2Front == "" {
		target, err := url.Parse(w.url)
		if err != nil {
			return err
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		original := proxy.Director
		proxy.Director = func(r *http.Request) {
			original(r)
			r.SetBasicAuth("admin", testAdminPassword)
		}
		certificate, leaf, err := p2TestCertificate()
		if err != nil {
			return err
		}
		front := httptest.NewUnstartedServer(proxy)
		front.TLS = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}
		front.StartTLS()
		w.closers = append(w.closers, front.Close)
		w.p2Front = front.URL
		base, err := w.filesDir()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(base, "p2-front.pem"), leaf, 0o600); err != nil {
			return err
		}
	}
	u, err := url.Parse(w.p2Front)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_P2_DESKTOP") == "1" {
		u.Host = strings.Replace(u.Host, "127.0.0.1", "host.docker.internal", 1)
	}
	u.Path = "/artifactory/" + key + "/"
	w.p2Repo = u.String()
	return nil
}

func (w *world) runP2(operation string) error {
	if w.p2Repo == "" {
		return fmt.Errorf("P2 repository URL is unset")
	}
	argv := []string{
		"-nosplash", "-consoleLog", "-configuration", "/work/p2-configuration",
		"-data", "/work/p2-workspace",
		"-application", "org.eclipse.equinox.p2.director",
		"-repository", w.p2Repo,
	}
	if operation == "list" {
		argv = append(argv, "-list", "org.eclipse.glsp.layout.source")
	} else {
		argv = append(argv,
			"-installIU", "org.eclipse.glsp.layout.source/2.7.0",
			"-destination", "/work/p2-install", "-profile", "AWP2Profile",
			"-p2.os", "linux", "-p2.ws", "gtk", "-p2.arch", "x86_64",
		)
	}
	return w.runContainer(w.p2Image, []string{"--entrypoint", "/usr/local/bin/p2-director"}, argv)
}

func p2TestCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "artifactweave P2 acceptance"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "host.docker.internal"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	privateDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	private := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})
	cert, err := tls.X509KeyPair(leaf, private)
	return cert, leaf, err
}

func (w *world) clearP2Install() error {
	base, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(base, "p2-install"))
}
