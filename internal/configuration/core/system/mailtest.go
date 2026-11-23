package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	stdmail "net/mail"
	"strings"
	"time"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
)

// Answers of 7.161.26's TestMailService, which `artifactweave mail test`
// reports as they are: an operator compares them with Artifactory's.
var (
	// ErrMailTestUnavailable is TestMailService.INVALID_CONFIG_ERROR_MSG: the
	// settings are missing, disabled or have no host.
	ErrMailTestUnavailable = errors.New("Mail server configuration must be enabled with a defined host and port.") //nolint:staticcheck,revive // Artifactory's exact sentence
	// ErrMailTestRecipient refuses an empty or malformed test recipient.
	ErrMailTestRecipient = errors.New("Test recipient field cannot be empty") //nolint:staticcheck,revive // Artifactory's exact sentence
)

// MailSender submits one message on its own connection; platform/mail's
// Sender is the production one.
type MailSender interface {
	Send(ctx context.Context, server mail.Server, message mail.Message, beforeData func(context.Context) error) error
}

// MailTest sends 7.x's "Send Test Email" message through the stored mail
// settings and sealed password: the same settings quota delivery reads, so a
// passing test shows those notifications' transport works. It reads the
// settings in Mail's own short transaction and only then opens SMTP.
type MailTest struct {
	Mail   configapi.Mail
	Sender MailSender
	// InTx, when set, refuses a caller already inside a metadata
	// transaction, which would otherwise be held across network I/O.
	InTx func(context.Context) bool
	Now  func() time.Time
}

// Send delivers the test message to recipient and returns TestMailService's
// confirmation. Missing or disabled settings answer ErrMailTestUnavailable
// without a connection; a refused or failed delivery answers the transport's
// redacted error, never a server response or credential.
func (m MailTest) Send(ctx context.Context, recipient string) (string, error) {
	if m.Mail == nil || m.Sender == nil {
		return "", fmt.Errorf("mail test unavailable: %w", kernel.ErrStore)
	}
	if m.InTx != nil && m.InTx(ctx) {
		return "", fmt.Errorf("mail test requires a transaction-free context: %w", kernel.ErrInvalid)
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" || strings.ContainsAny(recipient, "\r\n\x00") {
		return "", ErrMailTestRecipient
	}
	address, err := stdmail.ParseAddress(recipient)
	if err != nil {
		return "", fmt.Errorf("%w: invalid recipient address", ErrMailTestRecipient)
	}
	server, err := m.Mail.Server(ctx)
	if errors.Is(err, kernel.ErrNotFound) {
		return "", ErrMailTestUnavailable
	}
	if err != nil {
		return "", fmt.Errorf("read mail settings: %w", err)
	}
	if !server.Enabled || server.Host == "" || server.Port <= 0 {
		return "", ErrMailTestUnavailable
	}
	from := server.From
	if from == "" {
		from = mail.FallbackFrom
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	message := mail.Message{
		ID: hex.EncodeToString(id) + "@artifactweave.local", Date: now(),
		Subject: testSubject(server.SubjectPrefix), Text: testText(server.ArtifactoryURL),
		Recipients: []string{address.Address},
	}
	if err := m.Sender.Send(ctx, mail.Server{
		Host: server.Host, Port: server.Port, Username: server.Username, Password: server.Password,
		From: from, TLS: server.TLS, SSL: server.SSL,
	}, message, nil); err != nil {
		return "", fmt.Errorf("send test mail: %w", err)
	}
	return "A test message has been sent successfully to '" + address.Address + "'", nil
}

// testSubject is MailServiceImpl's subject: the prefix and a space before
// TestMailService's "Test", or "Test" alone without a prefix.
func testSubject(prefix string) string {
	if prefix == "" {
		return "Test"
	}
	return prefix + " Test"
}

// testText is MailUtils.getTestMessage as plain text: platform/mail sends
// text/plain, so the HTML line break and link become a newline and the URL.
func testText(url string) string {
	text := "This is a test message from Artifactory\n"
	if strings.TrimSpace(url) == "" {
		return text + "No Artifactory base URL is configured\n"
	}
	return text + "Your Artifactory base URL is: " + url + "\n"
}
