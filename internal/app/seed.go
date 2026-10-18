package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// authHash is the password hashing cost: the zero value, the production
// parameters, everywhere but this package's tests, which lower it in an init
// (hash_test.go). No configuration reaches it.
var authHash auth.HashParams

// InitialPasswordFile is where the first start writes a generated admin
// password, under the data directory. A file with mode 0600 rather than a
// log line, because logs are shipped and kept where the password must not be.
const InitialPasswordFile = "initial-admin-password"

// seedPrincipals creates the built-in principals on first start (decision
// 0005), and on a later start brings a store an earlier version seeded up to
// the current seed (auth.UpgradeSeed). The admin password is
// bootstrap.admin_password, or a generated one written to
// InitialPasswordFile.
func seedPrincipals(
	ctx context.Context,
	cfg config.Config,
	st store.Store,
	log *slog.Logger,
) error {
	seeded, err := auth.Seeded(ctx, st)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if seeded {
		removed, err := auth.UpgradeSeed(ctx, st, time.Now())
		if err != nil {
			return err //nolint:wrapcheck // auth wraps its own errors
		}
		if removed {
			// A warning, not an info line: requests without credentials
			// that read remote repositories through it are refused from now.
			log.Warn(
				"removed anonymous from the seeded Any Remote permission, as Artifactory does "+
					"since 7.84.3; grant anonymous a permission of its own where it needs one",
				"permission", auth.AnyRemotePermission,
			)
		}
		return nil
	}
	password, generated := cfg.Bootstrap.AdminPassword, false
	path := filepath.Join(cfg.DataDir, InitialPasswordFile)
	if password == "" {
		var b [24]byte
		_, _ = rand.Read(b[:])
		password, generated = base64.RawURLEncoding.EncodeToString(b[:]), true
		// Written before seeding: a password that reached only the database
		// would lock the operator out.
		if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		if err := os.WriteFile(path, []byte(password+"\n"), 0o600); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	hash, err := auth.HashPasswordWith(authHash, password)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	seededNow, err := auth.SeedHashed(ctx, st, hash, time.Now())
	if err != nil {
		return err //nolint:wrapcheck // auth wraps its own errors
	}
	switch {
	case !generated:
	case seededNow:
		log.Warn(
			"generated the admin password: sign in as admin with it, change it, then delete the file",
			"file",
			path,
		)
	default:
		// Another instance sharing the database seeded first, so this
		// password was never stored.
		_ = os.Remove(path)
	}
	return nil
}
