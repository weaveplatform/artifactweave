package app

import (
	"context"
	"fmt"
	"io"
	"time"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
)

// TestMail runs `artifactweave mail test <recipient>`: 7.x's "Send Test
// Email", which Artifactory serves only to its UI. It sends through the mail
// settings and sealed password stored in the store cfg names, as config
// apply works on that store, so it needs the server's configuration and
// master key but no running server or admin credential. The settings are
// read and the store's transaction finished before SMTP starts. sender is
// the transport; nil is platform/mail's Sender with system trust.
func TestMail(
	ctx context.Context,
	cfg config.Config,
	recipient string,
	sender system.MailSender,
	w io.Writer,
) error {
	if err := cfg.RequireMasterKey(); err != nil {
		return fmt.Errorf("mail test: %w", err)
	}
	keys, err := Keyring(cfg)
	if err != nil {
		return fmt.Errorf("mail test: %w", err)
	}
	st, err := OpenStore(ctx, cfg, appsql.Options{Keyring: keys})
	if err != nil {
		return fmt.Errorf("mail test: %w", err)
	}
	defer func() { _ = st.Close() }()
	if sender == nil {
		sender = mail.Sender{}
	}
	confirmation, err := system.MailTest{
		Mail: configurationOver(st, cfg.Configuration()).Mail, Sender: sender, InTx: st.InTx, Now: time.Now,
	}.Send(ctx, recipient)
	if err != nil {
		return fmt.Errorf("mail test: %w", err)
	}
	_, _ = fmt.Fprintln(w, confirmation)
	return nil
}
