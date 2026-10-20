package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	keyOne = "0123456789abcdef0123456789abcdef"
	keyTwo = "fedcba9876543210fedcba9876543210"
)

func TestKeyring(t *testing.T) {
	cfg := config.Default()
	if k, err := Keyring(cfg); k != nil || err != nil {
		t.Fatalf("no key = %v, %v", k, err)
	}
	cfg.MasterKey = keyOne
	k, err := Keyring(cfg)
	if err != nil || k.Active() != 1 {
		t.Fatalf("one key = %v, %v", k, err)
	}
	cfg.MasterKeyPrevious, cfg.MasterKey = keyOne, "v2:"+keyTwo
	k, err = Keyring(cfg)
	if err != nil || k.Active() != 2 || len(k.Versions()) != 2 {
		t.Fatalf("rotation pair = %v, %v", k, err)
	}
	cfg.MasterKeyPrevious = "short"
	if _, err := Keyring(cfg); !errors.Is(err, crypt.ErrWeakKey) {
		t.Fatalf("weak previous key: %v", err)
	}
	cfg.MasterKeyPrevious, cfg.MasterKey = keyOne, keyTwo
	if _, err := Keyring(cfg); !errors.Is(err, crypt.ErrBadVersion) {
		t.Fatalf("two version-1 keys: %v", err)
	}
}

func TestMigrateCommand(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "nested", "data")
	var out bytes.Buffer
	run := func(action string, to int) string {
		t.Helper()
		out.Reset()
		if err := Migrate(ctx, cfg, action, to, &out); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return out.String()
	}
	if got := run("version", 0); !strings.HasPrefix(got, "schema version 0 of ") {
		t.Fatalf("fresh version: %q", got)
	}
	if _, err := os.Stat(cfg.SQLitePath()); err != nil {
		t.Fatalf("database file not created under the data directory: %v", err)
	}
	if got := run(
		"up",
		0,
	); !strings.HasPrefix(got, "applied ") ||
		strings.HasPrefix(got, "applied 0") {
		t.Fatalf("up: %q", got)
	}
	if got := run("up", 0); !strings.HasPrefix(got, "applied 0 ") {
		t.Fatalf("second up: %q", got)
	}
	if got := run("down", -1); !strings.HasPrefix(got, "reverted 1 ") {
		t.Fatalf("down one step: %q", got)
	}
	if got := run("down", 0); strings.HasPrefix(got, "reverted 0 ") {
		t.Fatalf("down to 0: %q", got)
	}
	if got := run("version", 0); !strings.HasPrefix(got, "schema version 0 of ") {
		t.Fatalf("after down: %q", got)
	}
	if err := Migrate(ctx, cfg, "sideways", 0, &out); !errors.Is(err, ErrUnknownMigration) {
		t.Fatalf("unknown action: %v", err)
	}
}

func TestOpenStoreErrors(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://%zz"
	if _, err := OpenStore(ctx, cfg, sqlcommon.Options{}); err == nil {
		t.Fatal("a malformed PostgreSQL DSN opened")
	}
	if err := Migrate(ctx, cfg, "up", 0, &bytes.Buffer{}); err == nil {
		t.Fatal("migrate against an unopenable store succeeded")
	}
	// A data directory that is a file cannot hold the database.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = config.Default()
	cfg.DataDir = filepath.Join(file, "data")
	if _, err := OpenStore(ctx, cfg, sqlcommon.Options{}); err == nil {
		t.Fatal("a data directory under a file opened")
	}
	cfg.DataDir = file
	if _, err := OpenStore(ctx, cfg, sqlcommon.Options{}); err == nil {
		t.Fatal("a database path that is not a file opened")
	}
}

func TestRotateKeys(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	var out bytes.Buffer
	if err := RotateKeys(ctx, cfg, &out); !errors.Is(err, config.ErrMissingKey) {
		t.Fatalf("no master key: %v", err)
	}
	cfg.MasterKey = keyOne
	k, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(ctx, cfg, sqlcommon.Options{Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Workers().
		Create(ctx, types.Worker{Key: "w", Action: "BEFORE_DOWNLOAD"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Workers().PutSecret(ctx, "w", "token", "secret"); err != nil {
		t.Fatal(err)
	}
	tf := writeState(t, cfg, s, k)
	_ = s.Close()

	cfg.MasterKeyPrevious, cfg.MasterKey = keyOne, "v2:"+keyTwo
	if err := RotateKeys(ctx, cfg, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "re-sealed 1 value(s) under key version 2") ||
		!strings.Contains(got, "re-sealed 1 Terraform state(s) under key version 2") ||
		!strings.Contains(got, "remove master_key_previous") {
		t.Fatalf("output: %q", got)
	}
	cfg.MasterKeyPrevious = ""
	k, err = Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(ctx, cfg, sqlcommon.Options{Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got, err := s.Workers().Secrets(ctx, "w"); err != nil || got["token"] != "secret" {
		t.Fatalf("after rotation = %v, %v", got, err)
	}
	if got := readState(t, cfg, s, k, tf); got != tfState {
		t.Fatalf("after rotation the state reads %q", got)
	}

	cfg.MasterKey = "short"
	if err := RotateKeys(ctx, cfg, &out); !errors.Is(err, crypt.ErrWeakKey) {
		t.Fatalf("weak key: %v", err)
	}
	cfg.MasterKey = keyOne
	cfg.Database.Driver, cfg.Database.DSN = "postgres", "postgres://%zz"
	if err := RotateKeys(ctx, cfg, &out); err == nil {
		t.Fatal("rotation against an unopenable store succeeded")
	}
}
