package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalid refuses malformed local inputs before opening a connection.
	ErrInvalid = errors.New("invalid mail input")
	// ErrDelivery hides untrusted SMTP responses, which can echo secrets.
	ErrDelivery = errors.New("mail delivery failed")
	messageID   = regexp.MustCompile(`^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+$`)
)

// Server is a current transport snapshot. Never log it: Password is secret.
type Server struct {
	Host, Username, Password, From string
	Port                           int
	TLS, SSL                       bool
}

// Message belongs to one durable occurrence. The owner keeps ID and Date
// stable across retries and authorizes every recipient at submission time.
type Message struct {
	ID, Subject, Text string
	Date              time.Time
	Recipients        []string
}

// Sender submits a message on a fresh bounded connection. Zero Timeout means
// thirty seconds; nil RootCAs uses system trust. RootCAs must not be mutated
// while a Send is running.
type Sender struct {
	Timeout time.Duration
	RootCAs *x509.CertPool
}

// Send calls beforeData after connecting, negotiating encryption and preparing
// the envelope, but before sending DATA. The callback must finish its own short
// metadata transaction before returning. A positive DATA acknowledgement is
// success even if the subsequent QUIT fails, avoiding a needless duplicate.
func (s Sender) Send(ctx context.Context, server Server, message Message, beforeData func(context.Context) error) error {
	from, recipients, err := validate(server, message, s.Timeout)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(server.Host, strconv.Itoa(server.Port)))
	if err != nil {
		return deliveryError(ctx, "connect")
	}
	defer conn.Close() //nolint:errcheck // closing cannot retract acknowledged DATA
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return deliveryError(ctx, "deadline")
	}
	tlsConfig := &tls.Config{ServerName: server.Host, RootCAs: s.RootCAs, MinVersion: tls.VersionTLS12}
	transport := conn
	if server.SSL {
		encrypted := tls.Client(conn, tlsConfig)
		if err := encrypted.HandshakeContext(ctx); err != nil {
			return deliveryError(ctx, "TLS")
		}
		transport = encrypted
	}
	client, err := smtp.NewClient(transport, server.Host)
	if err != nil {
		return deliveryError(ctx, "greeting")
	}
	defer client.Close() //nolint:errcheck // the underlying connection is closed above
	if server.TLS && !server.SSL {
		if err := client.StartTLS(tlsConfig); err != nil {
			return deliveryError(ctx, "STARTTLS")
		}
	}
	if server.Username != "" || server.Password != "" {
		if err := client.Auth(smtp.PlainAuth("", server.Username, server.Password, server.Host)); err != nil {
			return deliveryError(ctx, "authentication")
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return deliveryError(ctx, "sender")
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return deliveryError(ctx, "recipient")
		}
	}
	if beforeData != nil {
		if err := beforeData(ctx); err != nil {
			return fmt.Errorf("mail submission refused: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mail submission cancelled: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return deliveryError(ctx, "DATA")
	}
	if _, err := io.Copy(writer, bytes.NewReader(encode(from, message))); err != nil {
		// Closing a partial DATA writer submits a truncated message. Close only
		// the connection on failure, so it cannot acknowledge partial content.
		return deliveryError(ctx, "body")
	}
	if err := writer.Close(); err != nil {
		return deliveryError(ctx, "acknowledgement")
	}
	_ = client.Quit()
	return nil
}

func deliveryError(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mail %s: %w", stage, err)
	}
	return fmt.Errorf("mail %s: %w", stage, ErrDelivery)
}

func validate(server Server, message Message, timeout time.Duration) (*stdmail.Address, []string, error) {
	bad := func() (*stdmail.Address, []string, error) { return nil, nil, ErrInvalid }
	if timeout < 0 || server.Host == "" || server.Port < 1 || server.Port > 65535 ||
		strings.ContainsAny(server.Host, "/\\@?#[]") ||
		strings.IndexFunc(server.Host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		strings.Contains(server.Host, ":") && net.ParseIP(server.Host) == nil ||
		strings.ContainsRune(server.Username+server.Password, '\x00') ||
		!messageID.MatchString(message.ID) || message.Date.IsZero() || len(message.Recipients) == 0 ||
		!utf8.ValidString(message.Text) || !header(message.Subject) || !header(server.From) {
		return bad()
	}
	from, err := stdmail.ParseAddress(server.From)
	if err != nil {
		return bad()
	}
	recipients := make([]string, 0, len(message.Recipients))
	for _, value := range message.Recipients {
		if !header(value) {
			return bad()
		}
		address, err := stdmail.ParseAddress(value)
		if err != nil {
			return bad()
		}
		recipients = append(recipients, address.Address)
	}
	return from, recipients, nil
}

func header(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func encode(from *stdmail.Address, message Message) []byte {
	var body strings.Builder
	body.WriteString("Date: " + message.Date.UTC().Format(time.RFC1123Z) + "\r\n")
	body.WriteString("Message-ID: <" + message.ID + ">\r\n")
	body.WriteString("From: " + from.String() + "\r\n")
	body.WriteString("To: undisclosed-recipients:;\r\n")
	body.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", message.Subject) + "\r\n")
	body.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(message.Text))
	for len(encoded) > 76 {
		body.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	body.WriteString(encoded + "\r\n")
	return []byte(body.String())
}
