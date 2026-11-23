package system

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
)

type fixedMail struct {
	server configapi.MailServer
	err    error
}

func (f fixedMail) Server(context.Context) (configapi.MailServer, error) { return f.server, f.err }

type recordingSender struct {
	server  mail.Server
	message mail.Message
	calls   int
	err     error
}

func (r *recordingSender) Send(_ context.Context, server mail.Server, message mail.Message, beforeData func(context.Context) error) error {
	r.calls++
	r.server, r.message = server, message
	if beforeData != nil {
		return errors.New("the test mail has no pre-DATA authority")
	}
	return r.err
}

var testServer = configapi.MailServer{
	Enabled: true, Host: "smtp.invalid", Port: 2525, Username: "user", Password: "mail-private-fixture",
	From: "sender@example.invalid", SubjectPrefix: "[JFrog]", ArtifactoryURL: "https://registry.invalid/", TLS: true,
}

func TestUnitMailTestSendsArtifactorysTestMessage(t *testing.T) {
	at := time.Date(2026, 11, 23, 1, 2, 3, 0, time.UTC)
	sender := &recordingSender{}
	got, err := MailTest{Mail: fixedMail{server: testServer}, Sender: sender, Now: func() time.Time { return at }}.
		Send(t.Context(), " Operator <operator@example.invalid> ")
	if err != nil || got != "A test message has been sent successfully to 'operator@example.invalid'" {
		t.Fatal(got, err)
	}
	want := mail.Server{Host: "smtp.invalid", Port: 2525, Username: "user", Password: "mail-private-fixture", From: "sender@example.invalid", TLS: true}
	if sender.server != want {
		t.Fatal("the stored transport was not used", sender.server.Host)
	}
	m := sender.message
	if m.Subject != "[JFrog] Test" || !m.Date.Equal(at) || len(m.Recipients) != 1 || m.Recipients[0] != "operator@example.invalid" ||
		m.Text != "This is a test message from Artifactory\nYour Artifactory base URL is: https://registry.invalid/\n" ||
		!strings.HasSuffix(m.ID, "@artifactweave.local") {
		t.Fatalf("message %+v", m)
	}
	server := testServer
	server.SubjectPrefix, server.ArtifactoryURL, server.From = "", "", ""
	if _, err := (MailTest{Mail: fixedMail{server: server}, Sender: sender}).Send(t.Context(), "operator@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if sender.message.Subject != "Test" || !strings.Contains(sender.message.Text, "No Artifactory base URL is configured") || sender.server.From != mail.FallbackFrom {
		t.Fatalf("defaults %+v %q", sender.message, sender.server.From)
	}
	if first, second := m.ID, sender.message.ID; first == second {
		t.Fatal("two test mails shared a Message-ID")
	}
}

func TestUnitMailTestTruthfulRefusals(t *testing.T) {
	disabled, hostless := testServer, testServer
	disabled.Enabled, hostless.Host = false, ""
	failure := errors.New("mail authentication: " + mail.ErrDelivery.Error())
	for name, tc := range map[string]struct {
		test      MailTest
		recipient string
		want      error
	}{
		"missing settings": {MailTest{Mail: fixedMail{err: kernel.ErrNotFound}}, "a@example.invalid", ErrMailTestUnavailable},
		"disabled":         {MailTest{Mail: fixedMail{server: disabled}}, "a@example.invalid", ErrMailTestUnavailable},
		"no host":          {MailTest{Mail: fixedMail{server: hostless}}, "a@example.invalid", ErrMailTestUnavailable},
		"store failure":    {MailTest{Mail: fixedMail{err: kernel.ErrStore}}, "a@example.invalid", kernel.ErrStore},
		"empty recipient":  {MailTest{Mail: fixedMail{server: testServer}}, "  ", ErrMailTestRecipient},
		"header injection": {MailTest{Mail: fixedMail{server: testServer}}, "a@example.invalid\r\nBcc: b@example.invalid", ErrMailTestRecipient},
		"bad recipient":    {MailTest{Mail: fixedMail{server: testServer}}, "not an address", ErrMailTestRecipient},
		"in transaction": {
			MailTest{Mail: fixedMail{server: testServer}, InTx: func(context.Context) bool { return true }},
			"a@example.invalid", kernel.ErrInvalid,
		},
		"delivery failure": {MailTest{Mail: fixedMail{server: testServer}, Sender: &recordingSender{err: failure}}, "a@example.invalid", failure},
		"unbound":          {MailTest{}, "a@example.invalid", kernel.ErrStore},
	} {
		t.Run(name, func(t *testing.T) {
			sender := tc.test.Sender
			if sender == nil && name != "unbound" {
				sender = &recordingSender{}
				tc.test.Sender = sender
			}
			_, err := tc.test.Send(t.Context(), tc.recipient)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "mail-private-fixture") {
				t.Fatal(err)
			}
			if r, ok := sender.(*recordingSender); ok && name != "delivery failure" && r.calls != 0 {
				t.Fatal("a refused test mail opened SMTP")
			}
		})
	}
}
