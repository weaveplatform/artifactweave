package system

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

type mailRunner struct{ failure error }

func (r mailRunner) Tx(ctx context.Context, fn func(context.Context) error) error {
	if r.failure != nil {
		return r.failure
	}
	return fn(ctx)
}

func (mailRunner) AfterCommit(_ context.Context, fn func()) { fn() }

type mailDocuments struct {
	configdoc.ConfigStore
	doc   configdoc.ConfigDocument
	err   error
	order *[]string
}

func (m mailDocuments) Get(context.Context, configdoc.ConfigKind, string) (configdoc.ConfigDocument, error) {
	if m.order != nil {
		*m.order = append(*m.order, "read")
	}
	return m.doc, m.err
}

func TestUnitMailSnapshotRequiresGuardAndDoesNotExposeCorruptDocuments(t *testing.T) {
	for _, tc := range []struct{ name, document string }{
		{"invalid JSON", "{"},
		{"null", "null"},
		{"wrong shape", "[]"},
		{"unsealed password", `{"host":"smtp.invalid","password":"private"}`},
		{"invalid persisted field", `{"host":"smtp.invalid","port":"25"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Mail{
				Configs: mailDocuments{doc: configdoc.ConfigDocument{Enabled: true, Document: []byte(tc.document)}},
				Tx:      mailRunner{},
				Guard:   func(context.Context) error { return nil },
			}
			server, err := m.Server(t.Context())
			if !errors.Is(err, kernel.ErrStore) || server.Password != "" || server.Host != "" {
				t.Fatal("corrupt mail configuration did not fail closed", err)
			}
		})
	}
	for name, m := range map[string]Mail{
		"no store":  {Tx: mailRunner{}, Guard: func(context.Context) error { return nil }},
		"no runner": {Configs: mailDocuments{}, Guard: func(context.Context) error { return nil }},
		"no guard":  {Configs: mailDocuments{}, Tx: mailRunner{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := m.Server(t.Context()); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("missing mail snapshot boundary was admitted", err)
			}
		})
	}
	failure := errors.New("mail snapshot fixture failure")
	for _, stage := range []string{"begin", "guard", "read"} {
		t.Run(stage, func(t *testing.T) {
			var order []string
			m := Mail{Tx: mailRunner{}, Configs: mailDocuments{order: &order}, Guard: func(context.Context) error { order = append(order, "guard"); return nil }}
			switch stage {
			case "begin":
				m.Tx = mailRunner{failure: failure}
			case "guard":
				m.Guard = func(context.Context) error { return failure }
			case "read":
				m.Configs = mailDocuments{order: &order, err: failure}
			}
			if _, err := m.Server(t.Context()); !errors.Is(err, failure) {
				t.Fatal("snapshot failure lost its identity", err)
			}
			if stage == "read" && !slices.Equal(order, []string{"guard", "read"}) || stage != "read" && len(order) != 0 {
				t.Fatal("mail configuration was read before its guard", order)
			}
		})
	}
}

func TestUnitMailServerValuesKeepDocumentedDefaultsAndIPv6(t *testing.T) {
	fields := map[string]any{
		"host": "2001:db8::1", "port": 2525, "tls": true, "ssl": true,
		"username": "relay", "from": "Sender <sender@example.invalid>", "artifactoryUrl": "https://registry.invalid/artifactory",
	}
	s, err := MailServerFields(fields)
	if err != nil || !s.Enabled || s.Host != "2001:db8::1" || s.Port != 2525 || !s.TLS || !s.SSL || s.SubjectPrefix != "[JFrog]" || s.Username != "relay" {
		t.Fatal("documented mail options were not retained", err)
	}
	if _, err := MailServerFields(map[string]any{"enabled": false}); err != nil {
		t.Fatal("disabled mail needs no active host", err)
	}
	if _, err := MailServerFields(map[string]any{"host": "smtp.invalid", "artifactoryUrl": "https://user:secret@registry.invalid"}); err == nil {
		t.Fatal("mail link admitted embedded credentials")
	}
	if _, err := MailServerFields(map[string]any{"host": "smtp.invalid", "port": float64(25)}); err != nil {
		t.Fatal("stored JSON whole port was refused", err)
	}
}
