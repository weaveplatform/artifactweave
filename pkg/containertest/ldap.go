package containertest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// openLDAPDigest pins osixia/openldap 1.5.0 (OpenLDAP 2.4.57), the index
// digest, which covers linux/amd64 and linux/arm64. It is a real slapd, so
// the LDAP suites bind and search a directory rather than a stand-in; bump
// it deliberately, here only, and check the second source serves it.
const openLDAPDigest = "sha256:18742e9c449c9c1afe129d3f2f3ee15fb34cc43e5f940a20f3399728f41d7c28"

// OpenLDAPImage is osixia/openldap 1.5.0.
const OpenLDAPImage = "osixia/openldap:1.5.0@" + openLDAPDigest

// openLDAPSources: mirror.gcr.io, Google's cache of Docker Hub, served the
// digest when it was pinned (checked 2026-11-23).
var openLDAPSources = []string{OpenLDAPImage, "mirror.gcr.io/osixia/openldap@" + openLDAPDigest}

// ffdhe2048 is RFC 7919's 2048-bit group. The image generates DH parameters
// on its first start when none are given, which takes minutes on a slow
// runner; a published group starts it at once.
const ffdhe2048 = `-----BEGIN DH PARAMETERS-----
MIIBCAKCAQEA//////////+t+FRYortKmq/cViAnPTzx2LnFg84tNpWp4TZBFGQz
+8yTnc4kmz75fS/jY2MMddj2gbICrsRhetPfHtXV/WVhJDP1H18GbtCFY2VVPe0a
87VXE15/V8k1mE8McODmi3fipona8+/och3xWKE2rec1MKzKT0g6eXq8CrGCsyT7
YdEIqUuyyOP7uWrat2DX9GgdT0Kj3jlN9K5W7edjcrsZCwenyO4KbXCeAvzhzffi
7MA0BM0oNC9hkXL+nOmFg/+OTxIy7vKBg8P+OxtMb61zO7X8vC7CIAXFjvGDfRaD
ssbzSibBsu/6iGtCOGEoXJf//////////wIBAg==
-----END DH PARAMETERS-----
`

// LDAPEndpoint addresses an OpenLDAP directory.
type LDAPEndpoint struct {
	// Host is the host the ports are published on, which the server's
	// certificate names.
	Host string
	// Port serves plain LDAP and TLSPort LDAPS.
	Port, TLSPort int
	// BaseDN is the directory's suffix, dc=example,dc=org.
	BaseDN string
	// AdminDN and AdminPassword bind as the directory's administrator, who
	// may write anything under BaseDN.
	AdminDN, AdminPassword string
	// ReaderDN and ReaderPassword bind as a user who may read every entry
	// but no password, a search manager's usual rights. The image's default
	// rules let anonymous clients read nothing.
	ReaderDN, ReaderPassword string
	// CA is the certificate authority that signed the server's certificate;
	// it is trusted by nothing else.
	CA *x509.CertPool
}

// URL is the directory's ldap:// URL with base DN base, or with none when
// base is "".
func (e LDAPEndpoint) URL(base string) string {
	return withBase(fmt.Sprintf("ldap://%s", net.JoinHostPort(e.Host, fmt.Sprint(e.Port))), base)
}

// TLSURL is the directory's ldaps:// URL with base DN base.
func (e LDAPEndpoint) TLSURL(base string) string {
	return withBase(fmt.Sprintf("ldaps://%s", net.JoinHostPort(e.Host, fmt.Sprint(e.TLSPort))), base)
}

func withBase(u, base string) string {
	if base == "" {
		return u
	}
	return u + "/" + base
}

// OpenLDAP starts OpenLDAP for t, with LDAPS under a certificate of its own
// CA, or skips t where Docker cannot run Linux containers.
func OpenLDAP(t *testing.T) LDAPEndpoint {
	t.Helper()
	RequireLinux(t)
	ep, stop, err := RunOpenLDAP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ep
}

// RunOpenLDAP starts OpenLDAP and returns its endpoint and a function that
// removes it, for callers without a *testing.T; check CheckLinux first. It
// returns once both ports accept a bind as the administrator.
func RunOpenLDAP(ctx context.Context) (LDAPEndpoint, func(), error) {
	image, err := requireImage(ctx, openLDAPSources...)
	if err != nil {
		return LDAPEndpoint{}, nil, fmt.Errorf("start openldap: %w", err)
	}
	ca, caPEM, certPEM, keyPEM, err := ldapCertificates()
	if err != nil {
		return LDAPEndpoint{}, nil, fmt.Errorf("openldap certificates: %w", err)
	}
	const certs = "/container/service/slapd/assets/certs/"
	file := func(name, content string) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{
			Reader: strings.NewReader(content), ContainerFilePath: certs + name, FileMode: 0o644,
		}
	}
	ep := LDAPEndpoint{BaseDN: "dc=example,dc=org", AdminDN: "cn=admin,dc=example,dc=org", CA: ca}
	ep.AdminPassword, ep.ReaderPassword = "admin-fixture-password", "reader-fixture-password"
	ep.ReaderDN = "cn=readonly,dc=example,dc=org"
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("389/tcp", "636/tcp"),
		testcontainers.WithEnv(map[string]string{
			"LDAP_ORGANISATION": "Example", "LDAP_DOMAIN": "example.org", "LDAP_ADMIN_PASSWORD": ep.AdminPassword,
			"LDAP_TLS": "true", "LDAP_TLS_CRT_FILENAME": "ldap.crt", "LDAP_TLS_KEY_FILENAME": "ldap.key",
			"LDAP_TLS_CA_CRT_FILENAME": "ca.crt", "LDAP_TLS_DH_PARAM_FILENAME": "dhparam.pem",
			"LDAP_TLS_VERIFY_CLIENT": "never", "LDAP_TLS_ENFORCE": "false",
			"LDAP_READONLY_USER": "true", "LDAP_READONLY_USER_USERNAME": "readonly",
			"LDAP_READONLY_USER_PASSWORD": ep.ReaderPassword,
		}),
		testcontainers.WithFiles(
			file("ca.crt", caPEM), file("ldap.crt", certPEM), file("ldap.key", keyPEM),
			file("dhparam.pem", ffdhe2048),
		),
		// The image copies the certificates it was given before it changes
		// their owner, which a read-only copy would refuse.
		testcontainers.WithCmdArgs("--copy-service"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("389/tcp").WithStartupTimeout(2*time.Minute),
			wait.ForListeningPort("636/tcp").WithStartupTimeout(2*time.Minute),
			wait.ForLog("slapd starting").WithStartupTimeout(2*time.Minute),
		),
	)
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return LDAPEndpoint{}, nil, fmt.Errorf("start openldap: %w", err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		stop()
		return LDAPEndpoint{}, nil, fmt.Errorf("openldap host: %w", err)
	}
	ep.Host = host
	for port, dst := range map[string]*int{"389/tcp": &ep.Port, "636/tcp": &ep.TLSPort} {
		mapped, err := ctr.MappedPort(ctx, port)
		if err != nil {
			stop()
			return LDAPEndpoint{}, nil, fmt.Errorf("openldap port %s: %w", port, err)
		}
		*dst = int(mapped.Num())
	}
	return ep, stop, nil
}

// ldapCertificates makes a CA and a server certificate it signs for the
// names a container's published port is reached by.
func ldapCertificates() (ca *x509.CertPool, caPEM, certPEM, keyPEM string, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", "", "", err //nolint:wrapcheck // named by the caller
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "containertest LDAP CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, "", "", "", err //nolint:wrapcheck // named by the caller
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, "", "", "", err //nolint:wrapcheck // named by the caller
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", "", "", err //nolint:wrapcheck // named by the caller
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, "", "", "", err //nolint:wrapcheck // named by the caller
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, "", "", "", err //nolint:wrapcheck // named by the caller
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	encode := func(kind string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
	}
	return pool, encode("CERTIFICATE", caDER), encode("CERTIFICATE", leafDER), encode("EC PRIVATE KEY", keyDER), nil
}
