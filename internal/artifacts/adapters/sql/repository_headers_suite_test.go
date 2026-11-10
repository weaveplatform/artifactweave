package sql_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

var errHeaderSnapshot = errors.New(
	"sqltest: repository document and header secret snapshots differ",
)

// headerKeys is the version-1 keyring the header suites seal with.
func headerKeys(t *testing.T) *crypt.Keyring {
	t.Helper()
	k, err := crypt.NewKeyring(
		crypt.MasterKey{Version: 1, Material: []byte(strings.Repeat("1", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestAccRepositoryHeaderSecrets runs repositoryHeaders on SQLite in memory
// and in a file and on PostgreSQL, each over the artifacts stream alone.
// Renaming an environment, which rewrites repository configurations through
// the access module's roles service, is the metadata store's half: its
// dialect tests of this name run it.
func TestAccRepositoryHeaderSecrets(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			repositoryHeaders(t, newStore(t))
		}, sqltest.WithPostgres(), sqltest.WithKeyring(headerKeys(t)))
}

// TestAccRepositoryHeaderMigration runs repositoryHeadersMigration on
// SQLite in memory and in a file and on PostgreSQL. The metadata store's
// dialect tests of this name check that its own open runs the conversion and
// its reseal covers the column.
func TestAccRepositoryHeaderMigration(t *testing.T) {
	keys := headerKeys(t)
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			repositoryHeadersMigration(t, newStore(t), keys)
		}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

// repositoryHeaders proves sealing and lifecycle behavior through both SQL
// backends and exercises the real configuration merge. Resolver revision behavior
// is covered by remote.TestAccResolverSealedCredentialsAndRotation; importing that
// service here would cycle through auth's SQL-backed seed tests.
func repositoryHeaders(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	config := repo.Config{
		Key:         "headers-remote",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://upstream.example",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-First", Value: "first-sensitive-value", Sensitive: true},
			{Name: "X-Second", Value: "second-sensitive-value", Sensitive: true},
			{Name: "X-Public", Value: "visible-value"},
			{Name: "X-Empty", Sensitive: true},
		},
	}
	headerMust(t, s.Repos().Create(ctx, config))
	if config.CustomHTTPHeaders[0].Value != "first-sensitive-value" {
		t.Fatal("document masking mutated input")
	}
	actual, err := s.Repos().Get(ctx, config.Key)
	headerMust(t, err)
	if diff := cmp.Diff(config, actual); diff != "" {
		t.Fatal(diff)
	}
	listed, err := s.Repos().List(ctx, port.RepoFilter{})
	headerMust(t, err)
	if len(listed) != 1 || cmp.Diff(config, listed[0]) != "" {
		t.Fatal("list failed to restore headers")
	}
	assertHeaderDocument(t, s, config.Key)

	stateBefore, err := s.RemoteCache().Source(ctx, config.Key)
	headerMust(t, err)

	// A normal read/update cycle preserves masked values through the existing
	// application merge; low-level Update remains an exact replacement.
	patch := map[string]json.RawMessage{
		"customHttpHeaders": json.RawMessage(
			`[{"name":"X-First","value":"","sensitive":true},{"name":"X-Second","value":"","sensitive":true},{"name":"X-Public","value":"new-visible","sensitive":false},{"name":"X-Empty","value":"","sensitive":true}]`,
		),
	}
	restored, err := repos.Merge(config, patch)
	headerMust(t, err)
	headerMust(t, s.Repos().Update(ctx, restored))
	got, err := s.Repos().Get(ctx, config.Key)
	headerMust(t, err)
	if got.CustomHTTPHeaders[0].Value != config.CustomHTTPHeaders[0].Value {
		t.Fatal("masked update erased secret")
	}

	// Renaming/reordering seals against the new header identity. Two entries with
	// the same name still have distinct positions and round-trip independently.
	got.CustomHTTPHeaders = []repo.CustomHTTPHeader{
		{Name: "X-Renamed", Value: "rotated-sensitive-value", Sensitive: true},
		{Name: "X-Renamed", Value: "duplicate-name-value", Sensitive: true},
		{Name: "X-Public", Value: "visible-value"},
	}
	headerMust(t, s.Repos().Update(ctx, got))
	roundTrip, err := s.Repos().Get(ctx, config.Key)
	headerMust(t, err)
	if cmp.Diff(got, roundTrip) != "" {
		t.Fatal("renamed/reordered headers changed")
	}
	assertHeaderDocument(t, s, config.Key)
	// Restore a retrieval-valid single header before checking the source fence.
	got.CustomHTTPHeaders = got.CustomHTTPHeaders[:1]
	headerMust(t, s.Repos().Update(ctx, got))
	stateAfter, err := s.RemoteCache().Source(ctx, config.Key)
	headerMust(t, err)
	if stateBefore.Generation == stateAfter.Generation {
		t.Fatal("header rotation did not fence old source")
	}

	// Recreating a configuration under another repository key reseals it. Moving
	// ciphertext instead fails authentication under the destination repository.
	other := got
	other.Key = "other-remote"
	headerMust(t, s.Repos().Create(ctx, other))
	original := readHeaderValue(t, s, config.Key, 0)
	copied := readHeaderValue(t, s, other.Key, 0)
	if string(original) == string(copied) {
		t.Fatal("repository copy reused ciphertext")
	}
	writeHeaderValue(t, s, other.Key, 0, original)
	if _, err = s.Repos().Get(ctx, other.Key); !errors.Is(err, crypt.ErrTampered) {
		t.Fatalf("cross-repository ciphertext accepted: %v", err)
	}
	if _, err = s.Repos().List(ctx, port.RepoFilter{}); !errors.Is(err, crypt.ErrTampered) {
		t.Fatalf("list accepted moved ciphertext: %v", err)
	}
	writeHeaderValue(t, s, other.Key, 0, copied)

	// Header identity is part of AAD as well as a checked document association.
	changedName := other
	changedName.CustomHTTPHeaders = append([]repo.CustomHTTPHeader(nil), other.CustomHTTPHeaders...)
	changedName.CustomHTTPHeaders[0].Name = "X-Moved"
	raw, err := json.Marshal(repo.Masked(changedName))
	headerMust(t, err)
	_, err = s.DB().
		ExecContext(ctx, s.Dialect().Rebind(`UPDATE repositories SET doc = ? WHERE key = ?`), string(raw), other.Key)
	headerMust(t, err)
	_, err = s.DB().
		ExecContext(ctx, s.Dialect().Rebind(`UPDATE repository_header_secrets SET name = ? WHERE repo_key = ?`), "X-Moved", other.Key)
	headerMust(t, err)
	if _, err = s.Repos().Get(ctx, other.Key); !errors.Is(err, crypt.ErrTampered) {
		t.Fatalf("renamed ciphertext accepted: %v", err)
	}
	// Delete can remove a corrupted secret without decrypting it.
	headerMust(t, s.Repos().Delete(ctx, other.Key))
	headerMust(t, s.Repos().Delete(ctx, config.Key))
	var count int
	headerMust(
		t,
		s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_header_secrets`).Scan(&count),
	)
	if count != 0 {
		t.Fatal("repository deletion retained header secrets")
	}
	headerMust(t, s.Repos().Create(ctx, config))
	if _, err = s.Repos().Get(ctx, config.Key); err != nil {
		t.Fatal(err)
	}
	config.CustomHTTPHeaders = nil
	headerMust(t, s.Repos().Update(ctx, config))
	headerMust(
		t,
		s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_header_secrets`).Scan(&count),
	)
	if count != 0 {
		t.Fatal("removed headers retained secrets")
	}
	repositoryHeaderSnapshots(t, s)
}

// repositoryHeadersMigration writes a repository whose document still holds
// its sensitive header values: the values in the document, no secret rows and
// header_secrets_version 0, the row the artifacts module's AfterMigrate hook
// converts. Reopening the store converts it, preserving the values and
// unrelated document fields while eliminating live plaintext, and a later
// open leaves it alone. A reopen is a second store over s's database
// (reopen), sealed with keys.
func repositoryHeadersMigration(t *testing.T, s *artsql.Store, keys *crypt.Keyring) {
	t.Helper()
	ctx := t.Context()
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
	var document map[string]any
	headerMust(t, json.Unmarshal(raw, &document))
	document["futureUnknownField"] = "preserved"
	raw, err = json.Marshal(document)
	headerMust(t, err)
	_, err = s.DB().ExecContext(ctx, s.Dialect().Rebind(
		`UPDATE repositories SET doc = ?, header_secrets_version = 0 WHERE key = ?`),
		string(raw), config.Key)
	headerMust(t, err)
	_, err = s.DB().ExecContext(ctx, s.Dialect().Rebind(
		`DELETE FROM repository_header_secrets WHERE repo_key = ?`), config.Key)
	headerMust(t, err)
	upgraded := reopen(t, s, keys)
	got, err := upgraded.Repos().Get(ctx, config.Key)
	headerMust(t, err)
	if diff := cmp.Diff(config, got); diff != "" {
		t.Fatal(diff)
	}
	assertHeaderDocument(t, upgraded, config.Key)
	var doc string
	headerMust(
		t,
		upgraded.DB().
			QueryRowContext(ctx, s.Dialect().Rebind(`SELECT doc FROM repositories WHERE key = ?`), config.Key).
			Scan(&doc),
	)
	if !strings.Contains(doc, "futureUnknownField") {
		t.Fatal("migration discarded an unrelated field")
	}
	ciphertext := readHeaderValue(t, upgraded, config.Key, 0)
	reopened := reopen(t, upgraded, keys)
	if string(readHeaderValue(t, reopened, config.Key, 0)) != string(ciphertext) {
		t.Fatal("completed migration repeated")
	}
	// Rotation discovers the new column, including databases initially opened
	// without encryption; standard Rotation covers retirement of the old key.
	if n, err := reseal(ctx, reopened); err != nil || n != 0 {
		t.Fatalf("reseal current header: %d, %v", n, err)
	}
}

func assertHeaderDocument(t *testing.T, s *artsql.Store, key string) {
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

func readHeaderValue(t *testing.T, s *artsql.Store, key string, position int) []byte {
	t.Helper()
	var value []byte
	headerMust(
		t,
		s.DB().
			QueryRowContext(t.Context(), s.Dialect().Rebind(`SELECT value FROM repository_header_secrets WHERE repo_key = ? AND position = ?`), key, position).
			Scan(&value),
	)
	return value
}

func writeHeaderValue(t *testing.T, s *artsql.Store, key string, position int, value []byte) {
	t.Helper()
	_, err := s.DB().
		ExecContext(t.Context(), s.Dialect().Rebind(`UPDATE repository_header_secrets SET value = ? WHERE repo_key = ? AND position = ?`), value, key, position)
	headerMust(t, err)
}

func headerMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func repositoryHeaderSnapshots(t *testing.T, s *artsql.Store) {
	t.Helper()
	config := repo.Config{
		Key:         "concurrent-headers",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		Description: "revision-0",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Revision", Value: "revision-0", Sensitive: true},
		},
	}
	headerMust(t, s.Repos().Create(t.Context(), config))
	var group sync.WaitGroup
	failures := make(chan error, 2)
	group.Go(func() {
		for n := 1; n <= 30; n++ {
			next := config
			next.CustomHTTPHeaders = append(
				[]repo.CustomHTTPHeader(nil),
				config.CustomHTTPHeaders...)
			next.Description = fmt.Sprintf("revision-%d", n)
			next.CustomHTTPHeaders[0].Value = next.Description
			if err := s.Repos().Update(t.Context(), next); err != nil {
				failures <- err
				return
			}
		}
	})
	group.Go(func() {
		for range 40 {
			got, err := s.Repos().Get(t.Context(), config.Key)
			if err != nil {
				failures <- err
				return
			}
			if got.Description != got.CustomHTTPHeaders[0].Value {
				failures <- errHeaderSnapshot
				return
			}
			listed, err := s.Repos().List(t.Context(), port.RepoFilter{})
			if err != nil {
				failures <- err
				return
			}
			for _, c := range listed {
				if c.Key == config.Key && c.Description != c.CustomHTTPHeaders[0].Value {
					failures <- errHeaderSnapshot
					return
				}
			}
		}
	})
	group.Wait()
	close(failures)
	for err := range failures {
		headerMust(t, err)
	}
}
