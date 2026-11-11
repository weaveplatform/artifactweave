package sql_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
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

func TestAccRepositoryHeaderWriteFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"INSERT", "DELETE"} {
		t.Run(operation, func(t *testing.T) {
			s := mustOpenFile(t, filepath.Join(t.TempDir(), "headers.db"), testKeys(t))
			config := headerConfig()
			if err := s.Repos().Create(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			sqltest.Exec(t, s.DB(), sqltest.FailTrigger(operation, "repository_header_secrets"))
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
				kernel.ErrNotFound,
			) {
				t.Fatal("failed create retained repository", err)
			}
		})
	}
}

func TestAccRepositoryHeaderCorruptionFailsClosed(t *testing.T) {
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
			s := mustOpenFile(t, filepath.Join(t.TempDir(), "headers.db"), testKeys(t))
			config := headerConfig()
			if err := s.Repos().Create(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			sqltest.Exec(t, s.DB(), mutation)
			if _, err := s.Repos().Get(t.Context(), config.Key); err == nil {
				t.Fatal("corrupt header read succeeded")
			}
			if got, err := s.Repos().
				List(t.Context(), port.RepoFilter{}); err == nil ||
				got != nil {
				t.Fatal("corrupt listing exposed partial configs", err)
			}
		})
	}
}

func TestAccRepositoryHeaderBackfillFailureAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	keys := testKeys(t)
	s := mustOpenFile(t, path, keys)
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
	sqltest.Exec(t, s.DB(), `DELETE FROM repository_header_secrets`)
	sqltest.Exec(t, s.DB(), sqltest.FailTrigger("INSERT", "repository_header_secrets"))
	s.Close()
	if _, err = openFile(t, path, keys); err == nil {
		t.Fatal("store opened after failed backfill")
	}
	raw := rawFile(t, path, keys)
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
	// The trigger sqltest.FailTrigger("INSERT", "repository_header_secrets") made.
	sqltest.Exec(t, raw.DB(), `DROP TRIGGER ops_fail_insert_repository_header_secrets`)
	raw.Close()
	retry := mustOpenFile(t, path, keys)
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

func TestAccRepositoryHeaderResealPlainValuesAndWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	s := mustOpenFile(t, path, nil)
	config := headerConfig()
	if err := s.Repos().Create(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	s.Close()
	encrypted := mustOpenFile(t, path, testKeys(t))
	if n, err := reseal(t.Context(), encrypted); err != nil || n != 2 {
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
	wrong := mustOpenFile(t, path, otherKeys(t))
	if _, err := wrong.Repos().Get(t.Context(), config.Key); !errors.Is(err, crypt.ErrWrongKey) {
		t.Fatal(err)
	}
	if _, err := reseal(t.Context(), wrong); err == nil {
		t.Fatal("wrong key resealed header")
	}
	without := mustOpenFile(t, path, nil)
	if _, err := without.Repos().
		Get(t.Context(), config.Key); !errors.Is(
		err,
		dialect.ErrNoKeyring,
	) {
		t.Fatal(err)
	}
}

func TestAccRepositoryHeaderBackfillRejectsWrongExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers.db")
	s := mustOpenFile(t, path, testKeys(t))
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
	for _, keys := range []*crypt.Keyring{nil, otherKeys(t)} {
		if _, err := openFile(t, path, keys); err == nil {
			t.Fatal("legacy header was sealed under an unverified key")
		}
	}
	correct := mustOpenFile(t, path, testKeys(t))
	got, err := correct.Repos().Get(t.Context(), config.Key)
	if err != nil || got.CustomHTTPHeaders[0].Value != "sensitive-value" {
		t.Fatal(err)
	}
}
