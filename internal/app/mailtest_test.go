package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
)

type capturedMail struct {
	server  mail.Server
	message mail.Message
	err     error
}

func (c *capturedMail) Send(_ context.Context, server mail.Server, message mail.Message, _ func(context.Context) error) error {
	c.server, c.message = server, message
	return c.err
}

// TestAccMailTestCommandSendsWithTheStoredSealedPassword covers the command
// over a real store: the password config apply sealed is the one the
// transport receives, and every refusal names its cause without it.
func TestAccMailTestCommandSendsWithTheStoredSealedPassword(t *testing.T) {
	cfg := testConfig(t)
	var out bytes.Buffer
	if err := TestMail(t.Context(), cfg, "operator@example.invalid", &capturedMail{}, &out); err == nil {
		t.Fatal("the mail test ran without a master key")
	}
	cfg.MasterKey = keyOne
	if err := TestMail(t.Context(), cfg, "operator@example.invalid", &capturedMail{}, &out); !errors.Is(err, system.ErrMailTestUnavailable) {
		t.Fatal("unconfigured mail did not refuse", err)
	}
	file := writeFile(t, "mailServer:\n  enabled: true\n  host: smtp.invalid\n  port: 2525\n"+
		"  username: mail-user\n  password: app-private-fixture\n  subjectPrefix: '[app]'\n")
	if err := ApplyDescriptor(t.Context(), cfg, file, false, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	sender := &capturedMail{}
	if err := TestMail(t.Context(), cfg, "operator@example.invalid", sender, &out); err != nil ||
		out.String() != "A test message has been sent successfully to 'operator@example.invalid'\n" {
		t.Fatal(err, out.String())
	}
	if sender.server.Password != "app-private-fixture" || sender.server.Host != "smtp.invalid" || sender.message.Subject != "[app] Test" {
		t.Fatal("the stored settings did not reach the transport")
	}
	out.Reset()
	failure := &capturedMail{err: errors.New("mail authentication: " + mail.ErrDelivery.Error())}
	if err := TestMail(t.Context(), cfg, "operator@example.invalid", failure, &out); err == nil ||
		strings.Contains(err.Error(), "app-private-fixture") || out.Len() != 0 {
		t.Fatal("a failed delivery reported success or exposed the password", err)
	}
	// The default transport is chosen before the settings refuse, so no
	// connection is attempted for disabled mail.
	if err := ApplyDescriptor(t.Context(), cfg, writeFile(t, "mailServer:\n  enabled: false\n"), false, &out); err != nil {
		t.Fatal(err)
	}
	if err := TestMail(t.Context(), cfg, "operator@example.invalid", nil, &out); !errors.Is(err, system.ErrMailTestUnavailable) {
		t.Fatal("disabled mail did not refuse", err)
	}
	broken := cfg
	broken.MasterKeyPrevious = "not-a-key"
	if err := TestMail(t.Context(), broken, "operator@example.invalid", nil, &out); err == nil {
		t.Fatal("an invalid previous master key did not fail")
	}
	cfg.DataDir = string([]byte{0})
	if err := TestMail(t.Context(), cfg, "operator@example.invalid", nil, &out); err == nil {
		t.Fatal("an unopenable store did not fail")
	}
}
