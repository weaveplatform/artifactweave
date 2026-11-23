package containertest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// mailpitDigest pins axllent/mailpit v1.32.0, the multi-platform index
// digest. It is a real SMTP server with STARTTLS and AUTH, so the mail suites
// deliver through an implementation that is not artifactweave's own; bump it
// deliberately, here only, and check the second source serves it.
const mailpitDigest = "sha256:0790692be935ea4420d085694f7985b1f692ca6a8771ab0e0bd8ba75ac180498"

// MailpitImage is axllent/mailpit v1.32.0.
const MailpitImage = "axllent/mailpit:v1.32.0@" + mailpitDigest

// mailpitSources: mirror.gcr.io, Google's cache of Docker Hub, served the
// digest when it was pinned (checked 2026-11-23).
var mailpitSources = []string{MailpitImage, "mirror.gcr.io/axllent/mailpit@" + mailpitDigest}

// MailpitEndpoint addresses a Mailpit SMTP server and its message API.
type MailpitEndpoint struct {
	// Host is the host the ports are published on, which the server's
	// certificate names.
	Host string
	// SMTPPort accepts plain SMTP and STARTTLS; it requires AUTH.
	SMTPPort int
	// Username and Password are the only credentials it accepts.
	Username, Password string
	// CA signed the server's certificate and is trusted by nothing else.
	CA  *x509.CertPool
	api string
}

// MailpitMessage is one received message as the server parsed it;
// Recipients holds the header and envelope recipients.
type MailpitMessage struct {
	From       string
	Recipients []string
	Subject    string
	Text       string
}

// Mailpit starts Mailpit for t, or skips t where Docker cannot run Linux
// containers.
func Mailpit(t *testing.T) MailpitEndpoint {
	t.Helper()
	RequireLinux(t)
	ep, stop, err := RunMailpit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ep
}

// RunMailpit starts Mailpit and returns its endpoint and a function that
// removes it, for callers without a *testing.T; check CheckLinux first. It
// returns once the SMTP port and the message API answer.
func RunMailpit(ctx context.Context) (MailpitEndpoint, func(), error) {
	image, err := requireImage(ctx, mailpitSources...)
	if err != nil {
		return MailpitEndpoint{}, nil, fmt.Errorf("start mailpit: %w", err)
	}
	ca, certPEM, keyPEM, err := serverCertificate("localhost")
	if err != nil {
		return MailpitEndpoint{}, nil, fmt.Errorf("mailpit certificates: %w", err)
	}
	ep := MailpitEndpoint{Username: "mail-fixture-user", Password: "mail-fixture-password", CA: ca}
	file := func(name, content string) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{Reader: strings.NewReader(content), ContainerFilePath: "/certs/" + name, FileMode: 0o644}
	}
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
		testcontainers.WithFiles(file("cert.pem", certPEM), file("key.pem", keyPEM)),
		testcontainers.WithEnv(map[string]string{
			"MP_SMTP_TLS_CERT": "/certs/cert.pem", "MP_SMTP_TLS_KEY": "/certs/key.pem",
			"MP_SMTP_AUTH": ep.Username + ":" + ep.Password,
			// The plain-SMTP case authenticates before STARTTLS; Go's client
			// sends PLAIN in clear only to localhost, as here.
			"MP_SMTP_AUTH_ALLOW_INSECURE": "true",
		}),
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForListeningPort("1025/tcp"), wait.ForHTTP("/livez").WithPort("8025/tcp"),
		).WithDeadline(2*time.Minute)),
	)
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return MailpitEndpoint{}, nil, fmt.Errorf("start mailpit: %w", err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		stop()
		return MailpitEndpoint{}, nil, fmt.Errorf("mailpit host: %w", err)
	}
	smtp, err := ctr.MappedPort(ctx, "1025/tcp")
	if err != nil {
		stop()
		return MailpitEndpoint{}, nil, fmt.Errorf("mailpit smtp port: %w", err)
	}
	api, err := ctr.MappedPort(ctx, "8025/tcp")
	if err != nil {
		stop()
		return MailpitEndpoint{}, nil, fmt.Errorf("mailpit api port: %w", err)
	}
	ep.Host, ep.SMTPPort = host, int(smtp.Num())
	ep.api = "http://" + net.JoinHostPort(host, api.Port())
	return ep, stop, nil
}

// Messages returns every received message, newest first.
func (e MailpitEndpoint) Messages(ctx context.Context) ([]MailpitMessage, error) {
	var list struct {
		Messages []struct{ ID string } `json:"messages"` //nolint:tagliatelle // Mailpit's names
	}
	if err := e.get(ctx, "/api/v1/messages", &list); err != nil {
		return nil, err
	}
	out := make([]MailpitMessage, 0, len(list.Messages))
	for _, m := range list.Messages {
		var full struct {
			From          struct{ Address string }
			To, Cc, Bcc   []struct{ Address string }
			Subject, Text string
		}
		if err := e.get(ctx, "/api/v1/message/"+url.PathEscape(m.ID), &full); err != nil {
			return nil, err
		}
		msg := MailpitMessage{From: full.From.Address, Subject: full.Subject, Text: full.Text}
		// The senders under test name recipients only in the envelope
		// (To: undisclosed-recipients), which Mailpit reports as Bcc.
		for _, to := range slices.Concat(full.To, full.Cc, full.Bcc) {
			msg.Recipients = append(msg.Recipients, to.Address)
		}
		out = append(out, msg)
	}
	return out, nil
}

// Clear deletes every received message.
func (e MailpitEndpoint) Clear(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.api+"/api/v1/messages", nil)
	if err != nil {
		return fmt.Errorf("mailpit clear: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailpit clear: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mailpit clear: status %d", resp.StatusCode)
	}
	return nil
}

func (e MailpitEndpoint) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.api+path, nil)
	if err != nil {
		return fmt.Errorf("mailpit %s: %w", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailpit %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mailpit %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("mailpit %s: %w", path, err)
	}
	return nil
}

// serverCertificate makes a CA, trusted by nothing else, and a server
// certificate it signs for host and 127.0.0.1, in PEM.
func serverCertificate(host string) (*x509.CertPool, string, string, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", "", fmt.Errorf("ca key: %w", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "artifactweave mail fixture CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, "", "", fmt.Errorf("ca certificate: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, "", "", fmt.Errorf("ca certificate: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", "", fmt.Errorf("server key: %w", err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, "", "", fmt.Errorf("server certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, "", "", fmt.Errorf("server key: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return pool, string(certPEM), string(keyPEM), nil
}
