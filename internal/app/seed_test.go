package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func adminPasswordIs(t *testing.T, st store.Store, password string) bool {
	t.Helper()
	u, err := st.Users().Get(context.Background(), auth.AdminUser)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := auth.CheckPassword(u.PasswordHash, password)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestSeedGeneratesAdminPassword(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.DataDir = filepath.Join(cfg.DataDir, "not-yet-created")
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	db := inmem.New()
	if err := seedPrincipals(ctx, cfg, db, log); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.DataDir, InitialPasswordFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("password file %v, %v", info, err)
	}
	b, _ := os.ReadFile(path)
	password := strings.TrimSpace(string(b))
	if len(password) < 32 || !adminPasswordIs(t, db, password) {
		t.Fatalf("generated password %q does not log admin in", password)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), path) ||
		strings.Contains(logs.String(), password) {
		t.Fatalf("log must name the file, never the password:\n%s", logs.String())
	}
	// A second start leaves the seeded store and the file alone.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := seedPrincipals(ctx, cfg, db, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("second start wrote a password file: %v", err)
	}
}

func TestSeedUsesConfiguredPassword(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "configured"
	db := inmem.New()
	if err := seedPrincipals(context.Background(), cfg, db, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if !adminPasswordIs(t, db, "configured") {
		t.Fatal("admin does not have the configured password")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, InitialPasswordFile)); !os.IsNotExist(err) {
		t.Fatalf("a configured password was also written to a file: %v", err)
	}
}

// hidden hides the seeded marker from its first reads, as if another
// instance seeded in between.
type hidden struct {
	store.Store
	hide atomic.Int32
}

func (h *hidden) Settings() store.SettingsStore { return hiddenSettings{h.Store.Settings(), h} }

type hiddenSettings struct {
	store.SettingsStore
	h *hidden
}

func (s hiddenSettings) Get(ctx context.Context, key string, into any) error {
	if key == types.SettingSeeded && s.h.hide.Add(-1) >= 0 {
		return store.ErrNotFound
	}
	return s.SettingsStore.Get(ctx, key, into) //nolint:wrapcheck // test double
}

func TestSeedLostRaceRemovesPasswordFile(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	if _, err := auth.Seed(ctx, db, "winner", time.Now()); err != nil {
		t.Fatal(err)
	}
	h := &hidden{Store: db}
	h.hide.Store(2) // our check and Seed's own check outside its transaction
	cfg := testConfig(t)
	if err := seedPrincipals(ctx, cfg, h, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, InitialPasswordFile)); !os.IsNotExist(err) {
		t.Fatalf("an unused password file was left behind: %v", err)
	}
	if !adminPasswordIs(t, db, "winner") {
		t.Fatal("the losing start changed the admin password")
	}
}

func TestSeedErrors(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := testConfig(t)
	cfg.DataDir = filepath.Join(file, "data")
	if err := seedPrincipals(ctx, cfg, inmem.New(), log); err == nil {
		t.Error("an uncreatable data directory was not reported")
	}

	cfg = testConfig(t)
	if err := os.Mkdir(filepath.Join(cfg.DataDir, InitialPasswordFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := seedPrincipals(ctx, cfg, inmem.New(), log); err == nil {
		t.Error("an unwritable password file was not reported")
	}

	db := inmem.New()
	if err := db.Groups().Create(ctx, types.Group{Name: auth.ReadersGroup}); err != nil {
		t.Fatal(err)
	}
	cfg = testConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	if err := seedPrincipals(ctx, cfg, db, log); err == nil {
		t.Error("a failed seed was not reported")
	}

	// openRuntime closes the store it opened when seeding fails.
	cfg = testConfig(t)
	cfg.Database.DSN = filepath.Join(t.TempDir(), "aw.db")
	cfg.DataDir = filepath.Join(file, "data")
	if _, err := openRuntime(ctx, cfg, log); err == nil {
		t.Error("openRuntime ignored a failed seed")
	}
}
