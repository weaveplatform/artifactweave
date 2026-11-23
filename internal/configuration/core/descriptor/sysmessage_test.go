package descriptor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

func TestUnitSystemMessageMergesFieldsAndStaysOutOfXML(t *testing.T) {
	db := inmem.New()
	api := NewAPI(depsOf(t, db))
	read := func() configapi.SystemMessage {
		t.Helper()
		m, err := api.System.SystemMessage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := read(); got != (configapi.SystemMessage{TitleColor: "#429f46"}) {
		t.Fatal("absent settings are not the disabled default", got)
	}
	const doc = "systemMessageConfig:\n  enabled: true\n  message: \"Maintenance at [https://status.invalid, status]\"\n  title: Notice\n"
	p, err := Parse([]byte(doc), noEnv)
	if err != nil || p.Changes != 3 {
		t.Fatal("system message not counted per field as movePlatformResources does", err)
	}
	if res := mustApply(t, db, doc); !slices.Equal(res.Created, []string{"systemMessageConfig"}) {
		t.Fatal("singleton not reported by section", res)
	}
	want := configapi.SystemMessage{Enabled: true, Message: "Maintenance at [https://status.invalid, status]", Title: "Notice", TitleColor: "#429f46"}
	if got := read(); got != want {
		t.Fatal("system message not stored", got)
	}
	if res := mustApply(t, db, doc); !slices.Equal(res.Unchanged, []string{"systemMessageConfig"}) {
		t.Fatal("reapply changed the system message", res)
	}
	mustApply(t, db, "systemMessageConfig:\n  titleColor: 99c27c\n")
	want.TitleColor = "99c27c"
	if got := read(); got != want {
		t.Fatal("partial update lost omitted fields", got)
	}
	mustApply(t, db, "systemMessageConfig:\n  enabled: false\n  title: ~\n  titleColor: ~\n")
	want = configapi.SystemMessage{Message: want.Message}
	if got := read(); got != want {
		t.Fatal("null did not clear, or a cleared colour returned to its default", got)
	}
	if res := mustApply(t, db, "systemMessageConfig:\n  title: ~\n"); !slices.Equal(res.Unchanged, []string{"systemMessageConfig"}) {
		t.Fatal("reapplying a clear rewrote the document", res)
	}
	if _, body := exportXML(t, depsOf(t, db)); strings.Contains(body, "systemMessage") || strings.Contains(body, "Maintenance") {
		t.Fatal("the XML export carries an element 7.161.26's XSD does not have")
	}
}

func TestUnitSystemMessageRefusals(t *testing.T) {
	for _, doc := range []string{
		"systemMessageConfig: ~\n",
		"systemMessageConfig: []\n",
		"systemMessageConfig: {showOnAllPages: true}\n",
		"systemMessageConfig: {enabled: ~}\n",
		"systemMessageConfig: {enabled: 'true'}\n",
		"systemMessageConfig: {message: 3}\n",
		"systemMessageConfig: {titleColor: [red]}\n",
	} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "systemMessageConfig") {
			t.Errorf("%q: %v", doc, err)
		}
	}
	db := inmem.New()
	if err := db.Configs().Put(context.Background(), configdoc.ConfigDocument{Kind: systemMessageKind, Key: systemMessageKey, Document: []byte("[")}); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemMessage(context.Background(), db.Configs()); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("corrupt stored system message was read", err)
	}
	if _, err := SystemMessage(context.Background(), faulty{Store: db, fail: "config get"}.Configs()); !errors.Is(err, errBoom) {
		t.Fatal("store failure hidden", err)
	}
}

func TestUnitMailServerNullDeletesSettingsAndSecret(t *testing.T) {
	db, ctx := inmem.New(), t.Context()
	d := depsOf(t, db)
	d.Projects = accesstest.Over(t, db).API().Projects
	api := NewAPI(d)
	mustApply(t, db, "mailServer:\n  host: smtp.invalid\n  password: mail-private-fixture\n")
	// The pinned provider's destroy body.
	if res := mustApply(t, db, "mailServer: ~\n"); !slices.Equal(res.Deleted, []string{"mailServer"}) || res.Changes != 1 {
		t.Fatal("mail server deletion not reported", res)
	}
	if _, err := api.Mail.Server(ctx); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("deleted mail server still delivers", err)
	}
	if _, err := db.Configs().Get(ctx, configdoc.ConfigMailServer, system.MailServerKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("document or sealed password survived", err)
	}
	if res := mustApply(t, db, "mailServer: ~\n"); !slices.Equal(res.Unchanged, []string{"mailServer"}) {
		t.Fatal("deleting absent mail settings was not a no-op", res)
	}
	if _, body := exportXML(t, d); strings.Contains(body, "mailServer") {
		t.Fatal("deleted mail server still exported")
	}
	// The pinned provider sends every field; a recreated server starts from
	// its own body with no retained password.
	mustApply(t, db, "mailServer:\n  enabled: true\n  host: smtp.invalid\n  port: 2525\n  password: ~\n")
	if server, err := api.Mail.Server(ctx); err != nil || server.Password != "" || server.Port != 2525 {
		t.Fatal("recreated mail server retained deleted state", err)
	}
}
