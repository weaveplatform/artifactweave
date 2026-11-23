package configuration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// TestAccMailTestDeliversThroughRealSMTP sends the mail test through a real
// SMTP server (Mailpit) from settings applied by the descriptor and read with
// the sealed password from SQLite and PostgreSQL: plain AUTH, STARTTLS
// verified against the fixture's own CA, and the failures an operator must
// see as failures: an implicit-TLS client on a STARTTLS port, a wrong
// password and an untrusted certificate.
func TestAccMailTestDeliversThroughRealSMTP(t *testing.T) {
	smtp := containertest.Mailpit(t)
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{9}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		mailDelivery(t, newStore(t), smtp)
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func mailDelivery(t *testing.T, db *appsql.Store, smtp containertest.MailpitEndpoint) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	storekit.Must(t, smtp.Clear(ctx))
	api := configurationAPI(t, db)
	settings := func(password string, tls, ssl bool) {
		applyConfiguration(t, api, ctx, fmt.Sprintf(`mailServer:
  enabled: true
  host: %s
  port: %d
  username: %s
  password: %q
  from: tls-sender@example.invalid
  subjectPrefix: '[TLS fixture]'
  artifactoryUrl: https://registry.invalid/
  tls: %t
  ssl: %t
`, smtp.Host, smtp.SMTPPort, smtp.Username, password, tls, ssl))
	}
	trusted := system.MailTest{Mail: api.Mail, Sender: mail.Sender{Timeout: 20 * time.Second, RootCAs: smtp.CA}, InTx: db.InTx}
	for _, tls := range []bool{false, true} {
		settings(smtp.Password, tls, false)
		recipient := fmt.Sprintf("starttls-%t@example.invalid", tls)
		if _, err := trusted.Send(ctx, recipient); err != nil {
			t.Fatal("configured mail did not deliver", tls, err)
		}
		received(t, ctx, smtp, recipient)
	}
	for name, failure := range map[string]struct {
		password string
		tls, ssl bool
		test     system.MailTest
		stage    string
	}{
		"implicit TLS on a STARTTLS port": {smtp.Password, false, true, trusted, "mail TLS"},
		"wrong password":                  {"wrong-mail-password", false, false, trusted, "mail authentication"},
		"untrusted certificate": {
			smtp.Password, true, false,
			system.MailTest{Mail: api.Mail, Sender: mail.Sender{Timeout: 20 * time.Second}},
			"mail STARTTLS",
		},
	} {
		settings(failure.password, failure.tls, failure.ssl)
		_, err := failure.test.Send(ctx, "refused@example.invalid")
		if !errors.Is(err, mail.ErrDelivery) || !strings.Contains(err.Error(), failure.stage) ||
			strings.Contains(err.Error(), failure.password) {
			t.Fatal(name, "did not fail truthfully", err)
		}
	}
	messages, err := smtp.Messages(ctx)
	storekit.Must(t, err)
	if len(messages) != 2 {
		t.Fatal("a failed mail test delivered a message", len(messages))
	}
}

func received(t *testing.T, ctx context.Context, smtp containertest.MailpitEndpoint, recipient string) {
	t.Helper()
	for {
		messages, err := smtp.Messages(ctx)
		storekit.Must(t, err)
		for _, m := range messages {
			if slices.Contains(m.Recipients, recipient) {
				if m.From != "tls-sender@example.invalid" || m.Subject != "[TLS fixture] Test" ||
					!strings.Contains(m.Text, "Your Artifactory base URL is: https://registry.invalid/") {
					t.Fatalf("received %+v", m)
				}
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("no test mail arrived for", recipient)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
