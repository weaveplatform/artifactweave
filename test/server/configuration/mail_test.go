package configuration_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccMailServerDescriptorUsesExistingPublicAuthorization(t *testing.T) {
	w := newRepoWorld(t)
	const yaml = "mailServer:\n  host: smtp.invalid\n  port: 2525\n  password: mail-private-fixture\n"
	if rec := w.do(http.MethodPatch, configPath, "reader", yaml, "Content-Type", "application/yaml"); rec.Code != http.StatusForbidden {
		t.Fatal("non-administrator changed mail configuration", rec.Code)
	}
	api := configurationAPI(t, w.db)
	if _, err := api.Mail.Server(t.Context()); err == nil {
		t.Fatal("denied patch left mail configuration")
	}
	rec := w.do(http.MethodPatch, configPath, "admin", yaml, "Content-Type", "application/yaml")
	if rec.Code != http.StatusOK || rec.Body.String() != "3 changes to config merged successfully" || strings.Contains(rec.Body.String(), "private") {
		t.Fatal("mail descriptor did not keep the configuration endpoint's existing answer", rec.Code)
	}
	server, err := api.Mail.Server(t.Context())
	if err != nil || server.Host != "smtp.invalid" || server.Port != 2525 || server.Password != "mail-private-fixture" {
		t.Fatal("public descriptor did not configure the internal delivery reader", err)
	}
}

func TestAccMailConfigurationSealedSQLAndCoherentReplacement(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		t.Run("sealing and clearing", func(t *testing.T) { mailSQLSecrets(t, newStore(t), keys) })
		t.Run("guarded replacement", func(t *testing.T) { mailSQLReplacement(t, newStore(t)) })
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func mailSQLSecrets(t *testing.T, db *appsql.Store, keys *crypt.Keyring) {
	t.Helper()
	ctx, api := t.Context(), configurationAPI(t, db)
	const secret = "mail-private-fixture"
	const yaml = "mailServer:\n  host: smtp.invalid\n  password: mail-private-fixture\n"
	applyConfiguration(t, api, ctx, yaml)
	var sealed []byte
	storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
		"SELECT value FROM config_secrets WHERE kind = ? AND key = ? AND name = ?"),
		"mail_server", "mailserver", "password").Scan(&sealed))
	if !crypt.IsSealed(sealed) {
		t.Fatal("mail password did not use the existing sealed envelope")
	}
	plain, err := keys.Open("config_secrets.value", "mail_server/mailserver/password", sealed)
	if err != nil || string(plain) != secret {
		t.Fatal("mail secret did not authenticate to its correct row identity", err)
	}
	var document string
	storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
		"SELECT document FROM config_documents WHERE kind = ? AND key = ?"), "mail_server", "mailserver").Scan(&document))
	if strings.Contains(document, "password") || strings.Contains(document, secret) {
		t.Fatal("mail password was written into public document JSON")
	}
	withoutKeys := configurationAPI(t, appsql.New(db.DB(), db.Dialect(), nil))
	if _, err := withoutKeys.Mail.Server(ctx); !errors.Is(err, dialect.ErrNoKeyring) {
		t.Fatal("sealed mail password was readable without its keyring", err)
	}
	for _, clear := range []string{"null", "''"} {
		applyConfiguration(t, api, ctx, "mailServer:\n  password: "+clear+"\n")
		var rows int
		storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
			"SELECT COUNT(*) FROM config_secrets WHERE kind = ? AND key = ? AND name = ?"), "mail_server", "mailserver", "password").Scan(&rows))
		if rows != 0 {
			t.Fatal("cleared mail password retained a secret row")
		}
		if result := applyConfiguration(t, api, ctx, "mailServer:\n  password: "+clear+"\n"); len(result.Unchanged) != 1 || len(result.Updated) != 0 {
			t.Fatal("reapplying cleared mail credentials rewrote the document")
		}
	}
}

type splitMailDocuments struct {
	configdoc.ConfigStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *splitMailDocuments) Get(ctx context.Context, kind configdoc.ConfigKind, key string) (configdoc.ConfigDocument, error) {
	first, err := s.ConfigStore.Get(ctx, kind, key)
	if err != nil {
		return first, err
	}
	var blocked bool
	s.once.Do(func() { blocked = true; close(s.entered) })
	if blocked {
		select {
		case <-s.release:
		case <-ctx.Done():
			return configdoc.ConfigDocument{}, ctx.Err()
		}
	}
	// Model the backend's separate document and secret statements. The root
	// guard must prevent replacement across this entire local snapshot.
	last, err := s.ConfigStore.Get(ctx, kind, key)
	if err != nil {
		return configdoc.ConfigDocument{}, err
	}
	first.Secrets = last.Secrets
	return first, nil
}

type mailDocumentStore struct {
	harness.Store
	documents configdoc.ConfigStore
}

func (s mailDocumentStore) Configs() configdoc.ConfigStore { return s.documents }

func mailSQLReplacement(t *testing.T, db *appsql.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	api := configurationAPI(t, db)
	applyConfiguration(t, api, ctx, "mailServer:\n  host: original.invalid\n  password: original-private\n")
	documents := &splitMailDocuments{ConfigStore: db.Configs(), entered: make(chan struct{}), release: make(chan struct{})}
	reader := configurationAPI(t, mailDocumentStore{Store: db, documents: documents})
	release := sync.OnceFunc(func() { close(documents.release) })
	type result struct {
		value configapi.MailServer
		err   error
	}
	done := make(chan result, 1)
	var running sync.WaitGroup
	defer func() { release(); cancel(); running.Wait() }()
	running.Go(func() { value, err := reader.Mail.Server(ctx); done <- result{value, err} })
	select {
	case <-documents.entered:
	case <-ctx.Done():
		t.Fatal("mail reader never reached its guarded document read")
	}
	plan, err := api.Descriptor.Parse([]byte("mailServer:\n  host: replacement.invalid\n  password: replacement-private\n"), nil)
	storekit.Must(t, err)
	blocked, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = api.Descriptor.Apply(blocked, plan, time.Now(), false)
	ended := blocked.Err()
	stop()
	if err == nil || !errors.Is(ended, context.DeadlineExceeded) {
		t.Fatal("mail replacement did not wait for the complete read snapshot", err)
	}
	release()
	select {
	case got := <-done:
		if got.err != nil || got.value.Host != "original.invalid" || got.value.Password != "original-private" {
			t.Fatal("mail snapshot mixed configuration revisions", got.err)
		}
	case <-ctx.Done():
		t.Fatal("mail reader did not release its transaction")
	}
	_, err = api.Descriptor.Apply(ctx, plan, time.Now(), false)
	storekit.Must(t, err)
	current, err := api.Mail.Server(ctx)
	if err != nil || current.Host != "replacement.invalid" || current.Password != "replacement-private" {
		t.Fatal("subsequent mail read did not refresh current settings", err)
	}
}
