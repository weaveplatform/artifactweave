package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
)

// ErrUnknownMigration reports a migrate action other than up, down or
// version.
var ErrUnknownMigration = errors.New("app: unknown migrate action")

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

// Migrate runs `artifactweave migrate <action>` over every module's stream,
// or over module's stream alone when module is set. "up" applies every
// pending migration. "down" reverts the migrations newer than to, which
// needs a module unless to is 0, everything; a negative to reverts one
// step, the newest migration of module's stream or, with no module, of the
// last stream in the canonical order that has any applied. "version"
// reports each stream's applied and latest versions, one line per stream.
// up and down write one line of outcome to w. A module that no stream has
// fails with sqlcommon.ErrUnknownModule.
func Migrate(
	ctx context.Context,
	cfg config.Config,
	action string,
	to int,
	module string,
	w io.Writer,
) error {
	if action != "up" && action != "down" && action != "version" {
		return fmt.Errorf("%w: %q", ErrUnknownMigration, action)
	}
	s, err := OpenStore(ctx, cfg, sqlcommon.Options{SkipMigrate: true})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	db, d, mods := s.DB(), s.Dialect(), sqlcommon.Modules()
	switch action {
	case "up":
		done, err := sqlcommon.Migrate(ctx, db, d, mods, module)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		_, _ = fmt.Fprintf(w, "applied %d migration(s): %v\n", len(done), done)
	case "down":
		if to < 0 {
			if module, to, err = oneStep(ctx, s, mods, module); err != nil {
				return fmt.Errorf("migrate down: %w", err)
			}
		}
		done, err := sqlcommon.Rollback(ctx, db, d, mods, module, to)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		_, _ = fmt.Fprintf(w, "reverted %d migration(s): %v\n", len(done), done)
	default:
		known := slices.ContainsFunc(mods, func(m dialect.Module) bool { return m.Name == module })
		if module != "" && !known {
			return fmt.Errorf("migrate version: %w %q", sqlcommon.ErrUnknownModule, module)
		}
		streams, err := sqlcommon.Version(ctx, db, d, mods)
		if err != nil {
			return fmt.Errorf("migrate version: %w", err)
		}
		for _, v := range streams {
			if module == "" || v.Module == module {
				_, _ = fmt.Fprintf(w, "schema version %d of %d (%s, %s)\n",
					v.Current, v.Latest, d.Name, v.Module)
			}
		}
	}
	return nil
}

// oneStep returns the stream and the target version that revert one step:
// the newest migration of module's stream or, with no module, of the last
// stream in the canonical order that has any applied. With nothing applied
// the target is 0, so the rollback reverts nothing; an unknown module is
// returned as it is, for Rollback to refuse.
func oneStep(
	ctx context.Context,
	s *sqlcommon.Store,
	mods []dialect.Module,
	module string,
) (string, int, error) {
	streams, err := sqlcommon.Version(ctx, s.DB(), s.Dialect(), mods)
	if err != nil {
		return "", 0, err //nolint:wrapcheck // the caller names the action
	}
	last := sqlcommon.StreamVersion{Module: module}
	for _, v := range streams {
		if v.Module == module || module == "" && v.Current > 0 {
			last = v
		}
	}
	return last.Module, max(last.Current-1, 0), nil
}

// RotateKeys runs `artifactweave keys rotate`: it opens the store with the
// configured keyring (master_key and master_key_previous), re-seals every
// sealed value under the newest version, and every sealed Terraform state
// in the blob store too (decision 0041), and reports how many changed.
// Once it has run, master_key_previous can be removed.
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
	states, err := resealStates(ctx, cfg, s, keys)
	if err != nil {
		return fmt.Errorf("keys rotate: %w", err)
	}
	_, _ = fmt.Fprintf(w, "re-sealed %d Terraform state(s) under key version %d\n",
		states.Resealed, keys.Active())
	for _, p := range states.Unreadable {
		_, _ = fmt.Fprintf(
			w,
			"the Terraform state %s opens under no configured key; it was lost already\n",
			p,
		)
	}
	if cfg.MasterKeyPrevious != "" {
		_, _ = fmt.Fprintln(
			w,
			"every value now opens with master_key alone; remove master_key_previous",
		)
	}
	return nil
}

// resealStates re-seals the Terraform states that an older key version
// sealed. They are blobs, not sealed columns, so the store's Reseal does not
// reach them, and without this a removed master_key_previous would lose
// them. A configuration without a hot blob store holds no states.
func resealStates(
	ctx context.Context,
	cfg config.Config,
	st *sqlcommon.Store,
	keys *crypt.Keyring,
) (terraformstate.ResealReport, error) {
	hot, _ := tiers(cfg, st)
	opened, err := hot.open(ctx, st, slog.New(slog.DiscardHandler))
	if err != nil || opened.store == nil {
		return terraformstate.ResealReport{}, err
	}
	svc := &terraformstate.Service{
		Items: &item.Service{
			Store:   st,
			Blobs:   opened.store,
			Formats: formatSettings(cfg, keys).For,
		},
		Keys: keys,
	}
	rep, err := svc.Reseal(ctx)
	if opened.mirror != nil {
		// A mirror's members may still be writing what Reseal stored.
		err = errors.Join(err, opened.mirror.WaitIdle(ctx))
	}
	return rep, err //nolint:wrapcheck // the services name themselves
}
