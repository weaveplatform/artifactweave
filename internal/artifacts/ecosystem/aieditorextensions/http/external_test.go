package aieditorextensionshttp_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

const (
	marketplaceHost = "marketplace.visualstudio.com"
	cdnHost         = "ms-vscode.gallerycdn.vsassets.io"
	vsixPath        = "assets/ms-vscode/hexeditor/1.11.1/Microsoft.VisualStudio.Services.VSIXPackage"
)

// marketplaceServer starts handler over TLS under a certificate for the
// Marketplace and its CDN, and returns options that resolve both hosts to
// it, trust the certificate and admit the loopback origins.
func marketplaceServer(
	t *testing.T,
	handler http.Handler,
	options upstream.ResolverOptions,
) (*httptest.Server, upstream.ResolverOptions) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: marketplaceHost},
		DNSNames:  []string{marketplaceHost, cdnHost},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	options.RootCAsPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	options.AllowedPrivateOrigins = []string{
		fmt.Sprintf("https://%s:%d", marketplaceHost, port),
		fmt.Sprintf("https://%s:%d", cdnHost, port),
	}
	options.LookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != marketplaceHost && host != cdnHost {
			return nil, fmt.Errorf("unexpected lookup of %s", host)
		}
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}
	return server, options
}

// configure changes the stored remote of f.
func (f *fixture) configure(change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), "editor-remote")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

// A Marketplace remote fetches an extension's files from the CDN the gallery
// names only while its external dependencies are on and a pattern matches
// the file's URL, as JFrog's Terraform provider (12.11.14) documents the two
// settings for this type; the gallery query, on the remote's own origin,
// answers either way. With bypassHeadRequests on, as 7.x keeps it for this
// type, each file takes one GET and no HEAD. The refusal's status is [I]:
// no 7.x source gives it (#1511).
func TestAccMarketplaceAssetsFollowTheExternalDependencySettings(t *testing.T) {
	query := []byte(
		`{"filters":[{"criteria":[{"filterType":7,"value":"ms-vscode.hexeditor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"},{"filterType":12,"value":"4096"}],"pageNumber":1,"pageSize":1,"sortBy":0,"sortOrder":0}],"assetTypes":[],"flags":950}`,
	)
	for _, tc := range []struct {
		name    string
		change  func(*repo.Config)
		fetched bool
	}{
		{"on and matched", func(*repo.Config) {}, true},
		{"off", func(c *repo.Config) { c.ExternalDependenciesEnabled = new(false) }, false},
		{"no pattern matches", func(c *repo.Config) {
			c.ExternalDependenciesPatterns = []string{"**/cdn.example/**"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixtureWith(t, true)
			// The default pattern names the CDN's hosts without a port; the
			// test server's port needs its own.
			f.configure(func(c *repo.Config) {
				c.ExternalDependenciesPatterns = []string{
					"**/" + strings.TrimPrefix(f.cdn, "https://") + "/**",
				}
				tc.change(c)
			})
			r := f.request(
				http.MethodPost,
				"_apis/public/gallery/extensionquery",
				"anonymous",
				query,
			)
			if r.Code != http.StatusOK || !strings.Contains(
				r.Body.String(),
				"/artifactory/api/aieditorextensions/editor-remote/assets/ms-vscode/hexeditor/1.11.1/",
			) {
				t.Fatalf("gallery: %d %s", r.Code, r.Body)
			}
			r = f.request(http.MethodGet, vsixPath, "anonymous", nil)
			switch {
			case tc.fetched && (r.Code != http.StatusOK || !bytes.Equal(r.Body.Bytes(), f.vsix)):
				t.Fatalf("VSIX: %d %s", r.Code, r.Body)
			case !tc.fetched && (r.Code != http.StatusBadGateway ||
				strings.TrimSpace(r.Body.String()) != `{"errors":[{"message":"Bad Gateway","status":502}]}`):
				t.Fatalf("VSIX: %d %s, want the refusal", r.Code, r.Body)
			}
			if want := map[bool]int32{true: 1, false: 0}[tc.fetched]; f.assets.Load() != want {
				t.Fatalf("CDN fetches %d, want %d", f.assets.Load(), want)
			}
		})
	}
}

// A file on the remote's own origin is the gallery's, not an external
// dependency: it is fetched with external dependencies off.
func TestAccSameOriginAssetsIgnoreTheExternalDependencySettings(t *testing.T) {
	f := newFixture(t)
	f.configure(func(c *repo.Config) { c.ExternalDependenciesEnabled = new(false) })
	r := f.request(http.MethodGet, vsixPath, "reader", nil)
	if r.Code != http.StatusOK || !bytes.Equal(r.Body.Bytes(), f.vsix) {
		t.Fatalf("VSIX: %d %s", r.Code, r.Body)
	}
}
