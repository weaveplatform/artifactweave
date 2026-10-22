package sqlcommon_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func headerConfig() repo.Config {
	return repo.Config{
		Key:         "headers",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://upstream.example",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{
				Name:      "X-Secret",
				Value:     "sensitive-value",
				Sensitive: true,
			},
			{Name: "X-Other", Value: "other-value", Sensitive: true},
		},
	}
}

func TestRepositoryHeaderWriteFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"INSERT", "DELETE"} {
		t.Run(operation, func(t *testing.T) {
			s := openContent(t, filepath.Join(t.TempDir(), "headers.db"), contentKeys(t))
			config := headerConfig()
			if err := s.Repos().Create(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			failBefore(t, s, operation, "repository_header_secrets")
			config.CustomHTTPHeaders[0].Value = "replacement-value"
			if err := s.Repos().Update(t.Context(), config); err == nil {
				t.Fatal("injected write failure ignored")
			}
			got, err := s.Repos().Get(t.Context(), config.Key)
			if err != nil || got.CustomHTTPHeaders[0].Value != "sensitive-value" {
				t.Fatal("failed update changed secret", err)
			}
			if operation != "INSERT" {
				return
			}
			config.Key = "new-repository"
			if err := s.Repos().Create(t.Context(), config); err == nil {
				t.Fatal("injected create failure ignored")
			}
			if _, err := s.Repos().
				Get(t.Context(), config.Key); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("failed create retained repository", err)
			}
		})
	}
}

func TestRepositoryHeaderCorruptionFailsClosed(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM repository_header_secrets WHERE position = 0`,
		`UPDATE repository_header_secrets SET name = 'wrong-name' WHERE position = 0`,
		`UPDATE repository_header_secrets SET position = 99 WHERE position = 0`,
		`UPDATE repository_header_secrets SET position = -1 WHERE position = 0`,
		`UPDATE repository_header_secrets SET value = (SELECT value FROM repository_header_secrets WHERE position = 1) WHERE position = 0`,
		`UPDATE repositories SET header_secrets_version = 0`,
		`UPDATE repositories SET doc = replace(doc, '"value":""', '"value":"older-writer-plaintext"')`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := openContent(t, filepath.Join(t.TempDir(), "headers.db"), contentKeys(t))
			config := headerConfig()
			if err := s.Repos().Create(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s, mutation)
			if _, err := s.Repos().Get(t.Context(), config.Key); err == nil {
				t.Fatal("corrupt header read succeeded")
			}
			if got, err := s.Repos().
				List(t.Context(), store.RepoFilter{}); err == nil ||
				got != nil {
				t.Fatal("corrupt listing exposed partial configs", err)
			}
		})
	}
}

func TestRepositoryHeaderBackfillFailureAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	keys := contentKeys(t)
	s := openContent(t, path, keys)
	config := headerConfig()
	if err := s.Repos().Create(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().
		ExecContext(t.Context(), `UPDATE repositories SET doc = ?, header_secrets_version = 0`, string(doc)); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s, `DELETE FROM repository_header_secrets`)
	failBefore(t, s, "INSERT", "repository_header_secrets")
	s.Close()
	if _, err = sqlite.Open(
		t.Context(),
		path,
		sqlite.Options{Options: sqlcommon.Options{Keyring: keys}},
	); err == nil {
		t.Fatal("store opened after failed backfill")
	}
	raw, err := sqlite.Open(
		t.Context(),
		path,
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true, Keyring: keys}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	var version, count int
	if err = raw.DB().
		QueryRowContext(t.Context(), `SELECT doc, header_secrets_version FROM repositories`).
		Scan(&stored, &version); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "sensitive-value") || version != 0 {
		t.Fatal("failed migration partially redacted document")
	}
	if err = raw.DB().
		QueryRowContext(t.Context(), `SELECT COUNT(*) FROM repository_header_secrets`).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatal("failed migration retained partial secrets", err)
	}
	execSQL(t, raw, `DROP TRIGGER fail_repository_header_secrets`)
	raw.Close()
	retry := openContent(t, path, keys)
	got, err := retry.Repos().Get(t.Context(), config.Key)
	if err != nil || got.CustomHTTPHeaders[0].Value != "sensitive-value" {
		t.Fatal("migration retry failed", err)
	}
	if err = retry.DB().
		QueryRowContext(t.Context(), `SELECT doc, header_secrets_version FROM repositories`).
		Scan(&stored, &version); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "sensitive-value") || version != 1 {
		t.Fatal("retry did not redact legacy document")
	}
}

func TestRepositoryHeaderResealPlainValuesAndWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	s := openContent(t, path, nil)
	config := headerConfig()
	if err := s.Repos().Create(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	s.Close()
	encrypted := openContent(t, path, contentKeys(t))
	if n, err := encrypted.Reseal(t.Context()); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	var sealed []byte
	if err := encrypted.DB().
		QueryRowContext(t.Context(), `SELECT value FROM repository_header_secrets WHERE position = 0`).
		Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !crypt.IsSealed(sealed) {
		t.Fatal("plaintext header not sealed")
	}
	encrypted.Close()
	wrong := openContent(t, path, contentKeysOther(t))
	if _, err := wrong.Repos().Get(t.Context(), config.Key); !errors.Is(err, crypt.ErrWrongKey) {
		t.Fatal(err)
	}
	if _, err := wrong.Reseal(t.Context()); err == nil {
		t.Fatal("wrong key resealed header")
	}
	without := openContent(t, path, nil)
	if _, err := without.Repos().
		Get(t.Context(), config.Key); !errors.Is(
		err,
		sqlcommon.ErrNoKeyring,
	) {
		t.Fatal(err)
	}
}

func TestRepositoryHeaderBackfillRejectsWrongExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	s := openContent(t, path, contentKeys(t))
	config := headerConfig()
	config.Password = "repository-password"
	if err := s.Repos().Create(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	config.Password = ""
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().
		ExecContext(t.Context(), `UPDATE repositories SET doc = ?, header_secrets_version = 0`, string(raw)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, keys := range []*crypt.Keyring{nil, contentKeysOther(t)} {
		if _, err := sqlite.Open(
			t.Context(),
			path,
			sqlite.Options{Options: sqlcommon.Options{Keyring: keys}},
		); err == nil {
			t.Fatal("legacy header was sealed under an unverified key")
		}
	}
	correct := openContent(t, path, contentKeys(t))
	got, err := correct.Repos().Get(t.Context(), config.Key)
	if err != nil || got.CustomHTTPHeaders[0].Value != "sensitive-value" {
		t.Fatal(err)
	}
}
