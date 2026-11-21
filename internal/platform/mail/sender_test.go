package mail

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"mime"
	"net"
	stdmail "net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type receiverOptions struct {
	implicit, startTLS, noAck, noQuit bool
	reject, stall                     string
}

type receiver struct {
	listener net.Listener
	tls      *tls.Config
	roots    *x509.CertPool
	options  receiverOptions
	messages chan []byte
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]bool
	auth     atomic.Int64
	data     atomic.Int64
}

func newReceiver(t *testing.T, options receiverOptions) *receiver {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &receiver{
		listener: listener, tls: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		roots: x509.NewCertPool(), options: options, messages: make(chan []byte, 20), done: make(chan struct{}), conns: map[net.Conn]bool{},
	}
	r.roots.AddCert(parsed)
	r.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns[conn] = true
			r.mu.Unlock()
			r.wg.Go(func() {
				defer conn.Close()
				defer func() { r.mu.Lock(); delete(r.conns, conn); r.mu.Unlock() }()
				r.serve(conn)
			})
		}
	})
	t.Cleanup(func() {
		close(r.done)
		_ = r.listener.Close()
		r.mu.Lock()
		for conn := range r.conns {
			_ = conn.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
	return r
}

func (r *receiver) serve(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if r.options.implicit {
		secure := tls.Server(conn, r.tls)
		if secure.HandshakeContext(context.Background()) != nil {
			return
		}
		conn = secure
	}
	if r.options.stall == "greeting" {
		<-r.done
		return
	}
	tp := textproto.NewConn(conn)
	if tp.PrintfLine("220 fixture SMTP") != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		command := strings.SplitN(line, " ", 2)[0]
		if command == r.options.stall {
			<-r.done
			return
		}
		if command == r.options.reject {
			_ = tp.PrintfLine("550 fixture refuses private-password-and-body")
			continue
		}
		switch command {
		case "EHLO":
			if tp.PrintfLine("250-fixture") != nil {
				return
			}
			if r.options.startTLS && tp.PrintfLine("250-STARTTLS") != nil {
				return
			}
			err = tp.PrintfLine("250 AUTH PLAIN")
		case "HELO", "MAIL", "RCPT", "RSET":
			err = tp.PrintfLine("250 accepted")
		case "AUTH":
			r.auth.Add(1)
			err = tp.PrintfLine("235 authenticated")
		case "STARTTLS":
			if !r.options.startTLS || tp.PrintfLine("220 ready for TLS") != nil {
				return
			}
			secure := tls.Server(conn, r.tls)
			if secure.HandshakeContext(context.Background()) != nil {
				return
			}
			conn, tp = secure, textproto.NewConn(secure)
		case "DATA":
			r.data.Add(1)
			if tp.PrintfLine("354 send message") != nil {
				return
			}
			body, readErr := tp.ReadDotBytes()
			if readErr != nil {
				return
			}
			r.messages <- body
			if r.options.noAck {
				return
			}
			err = tp.PrintfLine("250 message accepted")
		case "QUIT":
			if !r.options.noQuit {
				_ = tp.PrintfLine("221 goodbye")
			}
			return
		default:
			err = tp.PrintfLine("500 unknown command")
		}
		if err != nil {
			return
		}
	}
}

func (r *receiver) server(t *testing.T) Server {
	t.Helper()
	host, port, err := net.SplitHostPort(r.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return Server{Host: host, Port: n, From: "Artifactweave <sender@example.invalid>"}
}

func sampleMessage() Message {
	return Message{
		ID: "durable-warning@artifactweave.local", Date: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		Recipients: []string{"first@example.invalid", "Second <second@example.invalid>"},
		Subject:    "Quota threshold — project alpha", Text: strings.Repeat("Usage is 75 bytes of 100.\n", 12),
	}
}

func TestUnitSMTPDelivery(t *testing.T) {
	for _, mode := range []string{"plain", "STARTTLS", "SSL", "SSL and TLS", "QUIT disconnect", "HELO fallback"} {
		t.Run(mode, func(t *testing.T) {
			options := receiverOptions{startTLS: mode == "STARTTLS", implicit: strings.HasPrefix(mode, "SSL"), noQuit: mode == "QUIT disconnect"}
			if mode == "HELO fallback" {
				options.reject = "EHLO"
			}
			r := newReceiver(t, options)
			server := r.server(t)
			server.TLS, server.SSL = mode == "STARTTLS" || mode == "SSL and TLS", strings.HasPrefix(mode, "SSL")
			server.Username, server.Password = "fixture-user", "private-password-and-body"
			message := sampleMessage()
			checked := false
			err := (Sender{RootCAs: r.roots}).Send(t.Context(), server, message, func(context.Context) error {
				checked = true
				if r.data.Load() != 0 {
					t.Error("DATA preceded final authorization")
				}
				return nil
			})
			if err != nil || !checked || r.auth.Load() != 1 {
				t.Fatalf("delivery: error=%v checked=%v authenticated=%d", err, checked, r.auth.Load())
			}
			parsed, err := stdmail.ReadMessage(strings.NewReader(string(<-r.messages)))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Header.Get("Message-ID") != "<"+message.ID+">" || parsed.Header.Get("To") != "undisclosed-recipients:;" {
				t.Fatal("message identity or recipient privacy changed")
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
			if err != nil || subject != message.Subject {
				t.Fatal("subject did not round trip")
			}
			body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, parsed.Body))
			if err != nil || string(body) != message.Text {
				t.Fatal("message body did not round trip")
			}
		})
	}
}

func TestUnitSMTPRefusals(t *testing.T) {
	for _, stage := range []string{"AUTH", "MAIL", "RCPT", "DATA"} {
		t.Run(stage, func(t *testing.T) {
			r := newReceiver(t, receiverOptions{reject: stage})
			server := r.server(t)
			server.Username, server.Password = "fixture", "private-password-and-body"
			err := (Sender{}).Send(t.Context(), server, sampleMessage(), nil)
			if !errors.Is(err, ErrDelivery) || strings.Contains(err.Error(), server.Password) || r.data.Load() != 0 {
				t.Fatalf("refusal was not sanitized or submitted DATA: %v", err)
			}
		})
	}
	for _, mode := range []string{"untrusted STARTTLS", "untrusted SSL", "missing STARTTLS"} {
		t.Run(mode, func(t *testing.T) {
			r := newReceiver(t, receiverOptions{startTLS: mode == "untrusted STARTTLS", implicit: mode == "untrusted SSL"})
			server := r.server(t)
			server.SSL, server.TLS = mode == "untrusted SSL", mode != "untrusted SSL"
			if err := (Sender{}).Send(t.Context(), server, sampleMessage(), nil); !errors.Is(err, ErrDelivery) || r.data.Load() != 0 {
				t.Fatalf("encryption refusal: %v", err)
			}
		})
	}
	t.Run("revoked before DATA", func(t *testing.T) {
		r := newReceiver(t, receiverOptions{})
		refused := errors.New("current authority revoked")
		err := (Sender{}).Send(t.Context(), r.server(t), sampleMessage(), func(context.Context) error { return refused })
		if !errors.Is(err, refused) || r.data.Load() != 0 {
			t.Fatalf("revoked recipient received DATA: %v", err)
		}
	})
}

func TestUnitSMTPRetryIdentity(t *testing.T) {
	r := newReceiver(t, receiverOptions{noAck: true})
	for range 2 {
		if err := (Sender{}).Send(t.Context(), r.server(t), sampleMessage(), nil); !errors.Is(err, ErrDelivery) {
			t.Fatalf("uncertain acknowledgement: %v", err)
		}
	}
	first, second := <-r.messages, <-r.messages
	if string(first) != string(second) {
		t.Fatal("retry changed message identity, date or content")
	}
}

func TestUnitSMTPCancellation(t *testing.T) {
	for _, stage := range []string{"greeting", "DATA"} {
		t.Run(stage, func(t *testing.T) {
			r := newReceiver(t, receiverOptions{stall: stage})
			start := time.Now()
			err := (Sender{Timeout: 30 * time.Millisecond}).Send(t.Context(), r.server(t), sampleMessage(), nil)
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("network timeout did not bound %s: %v", stage, err)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		r := newReceiver(t, receiverOptions{stall: "greeting"})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := (Sender{}).Send(ctx, r.server(t), sampleMessage(), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation identity: %v", err)
		}
	})
}

func TestUnitSMTPInvalidInput(t *testing.T) {
	for _, field := range []string{"host", "port", "from", "from newline", "subject", "ID", "date", "recipients", "recipient", "recipient newline", "body", "credential", "timeout"} {
		t.Run(field, func(t *testing.T) {
			server := Server{Host: "127.0.0.1", Port: 1, From: "sender@example.invalid"}
			message, sender := sampleMessage(), Sender{}
			switch field {
			case "host":
				server.Host = "user@host"
			case "port":
				server.Port = 0
			case "from":
				server.From = "invalid"
			case "from newline":
				server.From += "\r\nBcc: other@example.invalid"
			case "subject":
				message.Subject += "\nX: injected"
			case "ID":
				message.ID += "\r\nX: injected"
			case "date":
				message.Date = time.Time{}
			case "recipients":
				message.Recipients = nil
			case "recipient":
				message.Recipients = []string{"invalid"}
			case "recipient newline":
				message.Recipients = []string{"valid@example.invalid\r\n"}
			case "body":
				message.Text = string([]byte{0xff})
			case "credential":
				server.Password = "bad\x00password"
			case "timeout":
				sender.Timeout = -time.Second
			}
			if err := sender.Send(t.Context(), server, message, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid input reached network: %v", err)
			}
		})
	}
}
