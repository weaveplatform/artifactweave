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

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/migration/access"
	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// authHash is the password hashing cost: the zero value, the production
// parameters, everywhere but this package's tests, which lower it in an init
// (hash_test.go). No configuration reaches it.
var authHash authn.HashParams

// InitialPasswordFile is where the first start writes a generated admin
// password, under the data directory. A file with mode 0600 rather than a
// log line, because logs are shipped and kept where the password must not be.
const InitialPasswordFile = "initial-admin-password"

// seedPrincipals creates the built-in principals on first start (decision
// 0005). The admin password is bootstrap.admin_password, or a generated one
// written to InitialPasswordFile.
func seedPrincipals(
	ctx context.Context,
	cfg config.Config,
	st store.Store,
	log *slog.Logger,
) error {
	seeded, err := authn.Seeded(ctx, st)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if seeded {
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
	hash, err := authn.HashPasswordWith(authHash, password)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	// The build-info repository is the artifacts module's, so the seed
	// creates it through the module's binding over the same store, which
	// joins the seed's transaction.
	repositories := repos.NewRepositories(st, access.KeysOf(st), access.ProjectsOf(st))
	seededNow, err := authn.SeedHashed(ctx, st, repositories, hash, time.Now())
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
