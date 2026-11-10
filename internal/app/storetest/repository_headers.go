package storetest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
)

// The artifacts module's tests run the repository header suites on its SQL
// adapter alone. What stays here needs the whole metadata store: the roles
// service of the access module, and the open and reseal that run every
// module's hooks and sealed columns.

// RepositoryHeaderEnvironmentRename checks that renaming an environment,
// which lists and rewrites whole repository configurations through the
// roles service, keeps a sensitive header's value and keeps it sealed.
func RepositoryHeaderEnvironmentRename(t *testing.T, open Opener) {
	t.Helper()
	keys := keyring(t, crypt.MasterKey{Version: 1, Material: []byte(strings.Repeat("1", 32))})
	repositoryHeaderEnvironmentRename(t, open(t, keys))
}

func repositoryHeaderEnvironmentRename(t *testing.T, s *appsql.Store) {
	t.Helper()
	ctx := t.Context()
	// The roles service lists and updates repositories and nothing more, so
	// the binding needs no key pairs or projects: those serve CheckKeypairs
	// and CheckEnvironments only. Building them with the access module root
	// would close an import cycle, as the authenticator's tests import this
	// package.
	service := roles.Service{Store: s, Repositories: repos.NewRepositories(s, nil, nil)}
	headerMust(t, service.CreateEnvironment(ctx, "QA"))
	config := repo.Config{
		Key:          "rewrite-headers",
		Class:        repo.Remote,
		PackageType:  repo.Generic,
		URL:          "https://upstream.example",
		Environments: []string{"QA"},
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Secret", Value: "first-sensitive-value", Sensitive: true},
		},
	}
	headerMust(t, s.Repos().Create(ctx, config))
	// Renaming an environment lists and rewrites entire repository configurations.
	// This unrelated edit must retain the restored secret and store it sealed.
	headerMust(t, service.RenameEnvironment(ctx, "QA", "STAGE"))
	config.Environments = []string{"STAGE"}
	got, err := s.Repos().Get(ctx, config.Key)
	headerMust(t, err)
	if diff := cmp.Diff(config, got); diff != "" {
		t.Fatal(diff)
	}
	assertHeaderDocument(t, s, config.Key)
	headerMust(t, s.Repos().Delete(ctx, config.Key))
	headerMust(t, service.DeleteEnvironment(ctx, "STAGE"))
}

// RepositoryHeadersOpen writes a repository whose document still holds its
// sensitive header values, with no secret rows and header_secrets_version 0,
// and reopens the store: the metadata store's open runs the artifacts
// module's AfterMigrate among every module's hooks, which converts the row,
// and the metadata store's Reseal, over every module's sealed columns, then
// finds nothing to change.
func RepositoryHeadersOpen(t *testing.T, open Opener) {
	t.Helper()
	ctx := t.Context()
	keys := keyring(t, crypt.MasterKey{Version: 1, Material: []byte(strings.Repeat("1", 32))})
	s := open(t, keys)
	config := repo.Config{
		Key:         "legacy",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://upstream.example",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Secret", Value: "legacy-sensitive-value", Sensitive: true},
			{Name: "X-Public", Value: "visible-value"},
			{Name: "X-Empty", Sensitive: true},
		},
	}
	headerMust(t, s.Repos().Create(ctx, config))
	raw, err := json.Marshal(config)
	headerMust(t, err)
	_, err = s.DB().ExecContext(ctx, s.Dialect().Rebind(
		`UPDATE repositories SET doc = ?, header_secrets_version = 0 WHERE key = ?`),
		string(raw), config.Key)
	headerMust(t, err)
	_, err = s.DB().ExecContext(ctx, s.Dialect().Rebind(
		`DELETE FROM repository_header_secrets WHERE repo_key = ?`), config.Key)
	headerMust(t, err)
	headerMust(t, s.Close())
	upgraded := open(t, keys)
	got, err := upgraded.Repos().Get(ctx, config.Key)
	headerMust(t, err)
	if diff := cmp.Diff(config, got); diff != "" {
		t.Fatal(diff)
	}
	assertHeaderDocument(t, upgraded, config.Key)
	if n, err := upgraded.Reseal(ctx); err != nil || n != 0 {
		t.Fatalf("reseal current header: %d, %v", n, err)
	}
}

func assertHeaderDocument(t *testing.T, s *appsql.Store, key string) {
	t.Helper()
	var doc string
	headerMust(
		t,
		s.DB().
			QueryRowContext(t.Context(), s.Dialect().Rebind(`SELECT doc FROM repositories WHERE key = ?`), key).
			Scan(&doc),
	)
	for _, secret := range []string{"first-sensitive-value", "second-sensitive-value", "rotated-sensitive-value", "duplicate-name-value", "legacy-sensitive-value"} {
		if strings.Contains(doc, secret) {
			t.Fatal("sensitive value remains in repository document")
		}
	}
	rows, err := s.DB().
		QueryContext(t.Context(), s.Dialect().Rebind(`SELECT value FROM repository_header_secrets WHERE repo_key = ?`), key)
	headerMust(t, err)
	defer rows.Close()
	for rows.Next() {
		var value []byte
		headerMust(t, rows.Scan(&value))
		if len(value) > 0 && !crypt.IsSealed(value) {
			t.Fatal("header secret is not sealed")
		}
	}
	headerMust(t, rows.Err())
}

func headerMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
