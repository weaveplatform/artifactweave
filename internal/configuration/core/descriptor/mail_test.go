package descriptor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

func TestUnitMailServerDescriptorMergesSealedSecretsAndEnablement(t *testing.T) {
	db, ctx := inmem.New(), t.Context()
	d := depsOf(t, db)
	d.Projects = accesstest.Over(t, db).API().Projects
	api := NewAPI(d)
	if _, err := api.Mail.Server(ctx); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("absent mail configuration was invented", err)
	}
	stamp := now
	patch := func(yaml string, dry bool) configapi.Result {
		t.Helper()
		plan, err := api.Descriptor.Parse([]byte(yaml), func(key string) (string, bool) { return "mail-private-fixture", key == "MAIL_PASSWORD" })
		if err != nil {
			t.Fatal(err)
		}
		stamp = stamp.Add(time.Second)
		result, err := api.Descriptor.Apply(ctx, plan, stamp, dry)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	const first = "mailServer:\n  host: smtp.invalid\n  password: ${MAIL_PASSWORD}\n  from: sender@example.invalid\n"
	patch(first, true)
	if _, err := api.Mail.Server(ctx); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("dry run stored a mail server", err)
	}
	result := patch(first, false)
	if !slices.Equal(result.Created, []string{"mailServer"}) {
		t.Fatal("singleton was reported as a named collection", result.Created)
	}
	settings, err := api.Mail.Server(ctx)
	if err != nil || !settings.Enabled || settings.Host != "smtp.invalid" || settings.Port != 25 || settings.SubjectPrefix != "[JFrog]" || settings.TLS || settings.SSL ||
		settings.Password != "mail-private-fixture" {
		t.Fatal("documented mail defaults or secret were lost", err)
	}
	stored, err := db.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey)
	if err != nil || bytes.Contains(stored.Document, []byte("password")) || bytes.Contains(stored.Document, []byte("mail-private-fixture")) {
		t.Fatal("password reached public document", err)
	}
	encoded, err := json.Marshal(stored)
	if err != nil || bytes.Contains(encoded, []byte("mail-private-fixture")) {
		t.Fatal("serializing configuration exposed its secret", err)
	}
	unchanged := patch(first, false)
	after, err := db.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey)
	if err != nil || len(unchanged.Unchanged) != 1 || !after.Updated.Equal(stored.Updated) {
		t.Fatal("reapplying the same mail server rewrote it", err)
	}
	patch("mailServer:\n  enabled: false\n  port: 2525\n  tls: true\n", false)
	settings, err = api.Mail.Server(ctx)
	if err != nil || settings.Enabled || settings.Port != 2525 || !settings.TLS || settings.Password != "mail-private-fixture" {
		t.Fatal("partial edit lost omitted settings or password", err)
	}
	patch("mailServer:\n  password: null\n", false)
	settings, err = api.Mail.Server(ctx)
	if err != nil || settings.Password != "" {
		t.Fatal("null password was retained", err)
	}
	patch("mailServer:\n  password: ''\n", false)
	if result := patch("mailServer:\n  password: ''\n", false); len(result.Unchanged) != 1 || len(result.Updated) != 0 {
		t.Fatal("empty mail password did not become a stable no-op")
	}
	// Repair a stored enablement mismatch even when document bytes match.
	stored, err = db.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey)
	if err != nil {
		t.Fatal(err)
	}
	stored.Enabled = true // its public document still says false
	if err := db.Configs().Put(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if result := patch("mailServer:\n  enabled: false\n", false); len(result.Updated) != 1 {
		t.Fatal("effective enablement correction was incorrectly unchanged")
	}
	stored, err = db.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey)
	if err != nil || stored.Enabled {
		t.Fatal("effective mail enablement was not synchronized", err)
	}
	rollback := errors.New("mail rollback fixture")
	err = db.Tx(ctx, func(ctx context.Context) error {
		plan, err := api.Descriptor.Parse([]byte("mailServer:\n  host: replacement.invalid\n  password: replacement-private\n"), noEnv)
		if err != nil {
			return err
		}
		if _, err := api.Descriptor.Apply(ctx, plan, stamp, false); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	settings, err = api.Mail.Server(ctx)
	if err != nil || settings.Host != "smtp.invalid" || settings.Password != "" || settings.Enabled {
		t.Fatal("outer rollback changed mail configuration or secret", err)
	}
}

func TestUnitMailServerDescriptorRejectsInvalidPatchesBeforeStorage(t *testing.T) {
	for _, yaml := range []string{
		"mailServer: []", "mailServer: {unknown: value}",
		"mailServer: {port: '25'}", "mailServer: {port: 1.5}", "mailServer: {port: 0}",
		"mailServer: {port: 65536}", "mailServer: {port: .nan}",
		"mailServer: {tls: 'true'}", "mailServer: {ssl: null}", "mailServer: {enabled: 1}",
		"mailServer: {host: 123}", "mailServer: {host: 'smtp.invalid:25'}", "mailServer: {host: 'https://smtp.invalid'}",
		"mailServer: {password: 123}", "mailServer: {from: 'invalid-address'}", "mailServer: {artifactoryUrl: 'relative/path'}",
		"mailServer: {subjectPrefix: \"bad\\r\\nBcc: other\"}",
	} {
		t.Run(yaml, func(t *testing.T) {
			_, err := Parse([]byte(yaml), noEnv)
			if !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid mail patch admitted", err)
			}
		})
	}
	db := inmem.New()
	p, err := Parse([]byte("mailServer: {enabled: true}"), noEnv)
	if err != nil {
		t.Fatal("partial mail settings must parse before merging", err)
	}
	if _, err = Apply(t.Context(), depsOf(t, db), p, now, false); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "host") {
		t.Fatal("enabled mail accepted without a host", err)
	}
	if _, err := db.Configs().Get(t.Context(), configdoc.ConfigMailServer, system.MailServerKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("invalid settings were persisted", err)
	}
}
