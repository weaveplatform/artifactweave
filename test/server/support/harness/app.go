package harness

import (
	"context"
	"log/slog"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// AppConfig returns the configuration App starts from: config.Default() with
// its data directory under t's temporary directory, so that each runtime has
// a store and blob tiers of its own.
func AppConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	return cfg
}

// AppOptions returns the costs App opens the runtime with: support.TestHash
// and the smallest RSA key Go accepts. Every runtime seeds the admin, builds
// an authenticator and generates a signing key, and production's costs made
// the suites take minutes under the race detector; verification reads the
// cost from each stored hash, so every password still checks.
func AppOptions() app.RuntimeOptions {
	return app.RuntimeOptions{PasswordHash: support.TestHash, TokenKeyBits: 1024}
}

// App opens the application runtime for t as serve opens it, over AppConfig(t)
// changed by mutate when mutate is not nil, with AppOptions' costs and a
// logger that discards, and closes it when t ends. A test that restarts the
// runtime opens it with app.OpenRuntime instead, since it closes the first
// runtime itself.
func App(t *testing.T, mutate func(*config.Config)) *app.Runtime {
	t.Helper()
	cfg := AppConfig(t)
	if mutate != nil {
		mutate(&cfg)
	}
	rt, err := app.OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), AppOptions())
	if err != nil {
		t.Fatal(err)
	}
	// t's context is cancelled before the cleanups run, and Close waits on
	// its context for mirror commits that are still running.
	t.Cleanup(func() {
		if err := rt.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return rt
}
