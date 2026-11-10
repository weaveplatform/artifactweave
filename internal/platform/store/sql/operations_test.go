package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// platformStore opens a SQLite store, sealed with the test keyring, that
// holds the platform's tables alone and one config document with a secret.
func platformStore(t *testing.T) *platsql.Store {
	t.Helper()
	s := platsql.New(sqltest.OpenSQLite(t, platsql.Module))
	if err := s.Configs().Put(context.Background(), configdoc.ConfigDocument{
		Kind:    configdoc.ConfigLDAP,
		Key:     "corp",
		Secrets: map[string]string{"manager_password": "mp"},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAccPlatformFaults injects a failure into the statement after a
// successful one and checks every multi-statement write and read reports
// it instead of half-applying or swallowing it.
func TestAccPlatformFaults(t *testing.T) {
	cases := []struct {
		name   string
		inject []string
		op     func(context.Context, *platsql.Store) error
	}{
		{
			"put config clear secrets",
			[]string{sqltest.FailTrigger("DELETE", "config_secrets")},
			func(ctx context.Context, s *platsql.Store) error {
				return s.Configs().
					Put(ctx, configdoc.ConfigDocument{Kind: configdoc.ConfigLDAP, Key: "corp"})
			},
		},
		{
			"put config secret",
			[]string{sqltest.FailTrigger("INSERT", "config_secrets")},
			func(ctx context.Context, s *platsql.Store) error {
				return s.Configs().Put(ctx, configdoc.ConfigDocument{
					Kind: configdoc.ConfigLDAP, Key: "corp", Secrets: map[string]string{"a": "b"},
				})
			},
		},
		{
			"get config secrets",
			[]string{sqltest.DropTable("config_secrets")},
			func(ctx context.Context, s *platsql.Store) error {
				_, err := s.Configs().Get(ctx, configdoc.ConfigLDAP, "corp")
				return err
			},
		},
		{
			"delete config secrets",
			[]string{sqltest.DropTable("config_secrets")},
			func(ctx context.Context, s *platsql.Store) error {
				return s.Configs().Delete(ctx, configdoc.ConfigLDAP, "corp")
			},
		},
		{
			"delete config",
			[]string{sqltest.FailTrigger("DELETE", "config_documents")},
			func(ctx context.Context, s *platsql.Store) error {
				return s.Configs().Delete(ctx, configdoc.ConfigLDAP, "corp")
			},
		},
		{
			"acquire lease update",
			[]string{sqltest.FailTrigger("UPDATE", "leases")},
			func(ctx context.Context, s *platsql.Store) error {
				_, err := s.Leases().
					Acquire(ctx, work.Lease{Name: "gc", Holder: "n", Expires: time.Unix(9, 0)}, time.Unix(1, 0))
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := platformStore(t)
			sqltest.Exec(t, s.DB(), c.inject...)
			if err := c.op(ctx, s); err == nil || errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("want an injected failure, got %v", err)
			}
		})
	}
}

// TestAccPlatformSealed reads the sealed platform columns through a second
// store over the same database without the keyring that sealed them: every
// read must refuse rather than return ciphertext.
func TestAccPlatformSealed(t *testing.T) {
	ctx := context.Background()
	sealed := platformStore(t)
	s := platsql.New(dialect.New(sealed.DB(), sealed.Dialect(), nil))
	for name, op := range map[string]func() error{
		"config": func() error { _, err := s.Configs().Get(ctx, configdoc.ConfigLDAP, "corp"); return err },
	} {
		if err := op(); !errors.Is(err, dialect.ErrNoKeyring) {
			t.Errorf("%s: want ErrNoKeyring, got %v", name, err)
		}
	}
}

// TestAccPlatformUnmigrated runs the platform's settings, lease and config
// methods against a database without the schema: each must surface the
// driver error, never a nil error or a misleading ErrNotFound.
func TestAccPlatformUnmigrated(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenDB(filepath.Join(t.TempDir(), "raw.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := platsql.New(dialect.New(db, sqlite.Dialect, nil))
	now := time.Unix(100, 0)
	ops := map[string]error{
		"get setting":    s.Settings().Get(ctx, "k", new(int)),
		"put setting":    s.Settings().Put(ctx, "k", 1),
		"delete setting": s.Settings().Delete(ctx, "k"),
		"setting keys":   sqltest.Err(s.Settings().Keys(ctx)),
		"acquire lease": sqltest.Err(
			s.Leases().Acquire(ctx, work.Lease{Name: "l", Holder: "h", Expires: now}, now),
		),
		"release lease": s.Leases().Release(ctx, "l", "h"),
		"list leases":   sqltest.Err(s.Leases().List(ctx)),
		"put config": s.Configs().
			Put(ctx, configdoc.ConfigDocument{Kind: configdoc.ConfigLDAP, Key: "k"}),
		"get config":    sqltest.Err(s.Configs().Get(ctx, configdoc.ConfigLDAP, "k")),
		"list config":   sqltest.Err(s.Configs().List(ctx, configdoc.ConfigLDAP)),
		"delete config": s.Configs().Delete(ctx, configdoc.ConfigLDAP, "k"),
	}
	for name, err := range ops {
		if err == nil || errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: want a driver error, got %v", name, err)
		}
	}
}
