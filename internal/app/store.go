package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
)

// ErrUnknownMigration reports a migrate action other than up, down or
// version.
var ErrUnknownMigration = errors.New("unknown migrate action")

// Keyring builds the keyring from master_key and, during a rotation,
// master_key_previous (crypt.ParseVersionedKey). It returns nil without
// error when no master key is configured, for the commands that run without
// one.
func Keyring(cfg config.Config) (*crypt.Keyring, error) {
	if cfg.MasterKey == "" {
		return nil, nil //nolint:nilnil // no key configured is a valid state for migrate
	}
	var keys []crypt.MasterKey
	for _, s := range []string{cfg.MasterKeyPrevious, cfg.MasterKey} {
		if s == "" {
			continue
		}
		mk, err := crypt.ParseVersionedKey(s)
		if err != nil {
			return nil, fmt.Errorf("master key: %w", err)
		}
		keys = append(keys, mk)
	}
	k, err := crypt.NewKeyring(keys...)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	return k, nil
}

// OpenStore opens the configured metadata store. The SQLite file's
// directory is created when missing, so a first start needs nothing but a
// data directory path.
func OpenStore(
	ctx context.Context,
	cfg config.Config,
	o sqlcommon.Options,
) (*sqlcommon.Store, error) {
	if cfg.Database.Driver == "postgres" {
		s, err := postgres.Open(ctx, cfg.Database.DSN, postgres.Options{Options: o})
		if err != nil {
			return nil, fmt.Errorf("open store: %w", err)
		}
		return s, nil
	}
	path := cfg.SQLitePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	s, err := sqlite.Open(ctx, path, sqlite.Options{Options: o})
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return s, nil
}

// Migrate runs `artifactweave migrate <action>`: "up" applies every pending
// migration, "down" reverts those newer than to (one step when to is
// negative, everything when it is 0), and "version" reports the applied and
// latest versions. It writes one line of outcome to w.
func Migrate(ctx context.Context, cfg config.Config, action string, to int, w io.Writer) error {
	if action != "up" && action != "down" && action != "version" {
		return fmt.Errorf("%w: %q", ErrUnknownMigration, action)
	}
	s, err := OpenStore(ctx, cfg, sqlcommon.Options{SkipMigrate: true})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	db, d := s.DB(), s.Dialect()
	switch action {
	case "up":
		done, err := sqlcommon.Migrate(ctx, db, d)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		_, _ = fmt.Fprintf(w, "applied %d migration(s): %v\n", len(done), done)
	case "down":
		if to < 0 {
			cur, _, err := sqlcommon.Version(ctx, db, d)
			if err != nil {
				return fmt.Errorf("migrate down: %w", err)
			}
			to = max(cur-1, 0)
		}
		done, err := sqlcommon.Rollback(ctx, db, d, to)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		_, _ = fmt.Fprintf(w, "reverted %d migration(s): %v\n", len(done), done)
	default:
		cur, latest, err := sqlcommon.Version(ctx, db, d)
		if err != nil {
			return fmt.Errorf("migrate version: %w", err)
		}
		_, _ = fmt.Fprintf(w, "schema version %d of %d (%s)\n", cur, latest, d.Name)
	}
	return nil
}

// RotateKeys runs `artifactweave keys rotate`: it opens the store with the
// configured keyring (master_key and master_key_previous), re-seals every
// sealed value under the newest version and reports how many changed. Once
// it has run, master_key_previous can be removed.
func RotateKeys(ctx context.Context, cfg config.Config, w io.Writer) error {
	if err := cfg.RequireMasterKey(); err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	keys, err := Keyring(cfg)
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	s, err := OpenStore(ctx, cfg, sqlcommon.Options{Keyring: keys})
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	defer func() { _ = s.Close() }()
	n, err := s.Reseal(ctx)
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	_, _ = fmt.Fprintf(w, "re-sealed %d value(s) under key version %d\n", n, keys.Active())
	if cfg.MasterKeyPrevious != "" {
		_, _ = fmt.Fprintln(
			w,
			"every value now opens with master_key alone; remove master_key_previous",
		)
	}
	return nil
}
