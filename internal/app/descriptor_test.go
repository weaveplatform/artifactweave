package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/config"
	"github.com/weaveplatform/artifactweave/internal/descriptor"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "descriptor.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// repoKeys opens cfg's store and lists its repositories.
func repoKeys(t *testing.T, cfg config.Config) []string {
	t.Helper()
	keys, err := Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(context.Background(), cfg, sqlcommon.Options{Keyring: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rs, err := st.Repos().List(context.Background(), store.RepoFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.Key)
	}
	return out
}

func TestApplyDescriptorCommand(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.MasterKey = keyOne
	file := writeFile(t, "localRepositories:\n  cli-local:\n    type: generic\n")
	var out bytes.Buffer
	if err := ApplyDescriptor(ctx, cfg, file, true, &out); err != nil ||
		!strings.Contains(out.String(), "Dry run") || len(repoKeys(t, cfg)) != 0 {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := ApplyDescriptor(ctx, cfg, file, false, &out); err != nil ||
		!strings.Contains(out.String(), "Created: localRepositories.cli-local") {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	if got := repoKeys(t, cfg); strings.Join(got, ",") != "cli-local" {
		t.Fatalf("repositories %v", got)
	}
	out.Reset()
	if err := ApplyDescriptor(ctx, cfg, file, false, &out); err != nil ||
		!strings.Contains(out.String(), "Unchanged: localRepositories.cli-local") {
		t.Fatalf("second apply: %v\n%s", err, out.String())
	}
}

func TestApplyDescriptorCommandErrors(t *testing.T) {
	ctx := context.Background()
	good := writeFile(t, "localRepositories:\n  x:\n    type: generic\n")
	for name, c := range map[string]struct {
		tune func(*config.Config)
		file string
	}{
		"no master key":  {func(c *config.Config) { c.MasterKey = "" }, good},
		"bad master key": {func(c *config.Config) { c.MasterKey = "short" }, good},
		"missing file":   {nil, filepath.Join(t.TempDir(), "absent.yaml")},
		"invalid":        {nil, writeFile(t, "localRepositories:\n  x:\n    nonsense: 1\n")},
		"refused":        {nil, writeFile(t, "localRepositories:\n  artifactory-build-info: null\n")},
		"bad database":   {func(c *config.Config) { c.Database.Driver, c.Database.DSN = "postgres", "postgres://%zz" }, good},
	} {
		cfg := testConfig(t)
		cfg.MasterKey = keyOne
		if c.tune != nil {
			c.tune(&cfg)
		}
		if err := ApplyDescriptor(ctx, cfg, c.file, false, io.Discard); err == nil ||
			!strings.HasPrefix(err.Error(), "config apply:") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBootstrapDescriptorAppliesOnce(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.Bootstrap.Descriptor = writeFile(
		t,
		"localRepositories:\n  boot-local:\n    type: generic\n",
	)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.Repos().Get(ctx, "boot-local"); err != nil {
		t.Fatalf("bootstrap repository: %v", err)
	}
	var marker bootstrapMarker
	if err := rt.store.Settings().Get(ctx, types.SettingBootstrapDescriptor, &marker); err != nil ||
		marker.File != cfg.Bootstrap.Descriptor || len(marker.SHA256) != 64 {
		t.Fatalf("marker %+v %v", marker, err)
	}
	// An operator deletes the repository; a later start must not bring it
	// back, even with a changed file.
	if err := rt.store.Repos().Delete(ctx, "boot-local"); err != nil {
		t.Fatal(err)
	}
	if err := rt.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Bootstrap.Descriptor,
		[]byte("localRepositories:\n  later-local:\n    type: generic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err = openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.close(ctx) }()
	for _, key := range []string{"boot-local", "later-local"} {
		if _, err := rt.store.Repos().Get(ctx, key); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the bootstrap descriptor was applied again: %s: %v", key, err)
		}
	}
	if !strings.Contains(logs.String(), "applied the bootstrap descriptor") {
		t.Fatalf("logs:\n%s", logs.String())
	}
}

func TestBootstrapDescriptorFailureStopsStart(t *testing.T) {
	ctx := context.Background()
	for name, body := range map[string]string{
		"missing": "",
		"invalid": "localRepositories:\n  x:\n    nonsense: 1\n",
		"refused": "localRepositories:\n  artifactory-build-info: null\n",
		// Artifactory's own bootstrap file is not the PATCH format.
		"import file": "version: 1\nGeneralConfiguration:\n  baseUrl: https://example.test\n",
	} {
		cfg := testConfig(t)
		cfg.Bootstrap.Descriptor = filepath.Join(t.TempDir(), "absent.yaml")
		if body != "" {
			cfg.Bootstrap.Descriptor = writeFile(t, body)
		}
		if _, err := openRuntime(
			ctx,
			cfg,
			slog.New(slog.NewTextHandler(io.Discard, nil)),
		); err == nil ||
			!strings.Contains(err.Error(), "bootstrap descriptor") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The descriptor package's own error survives the wrapping.
	cfg := testConfig(t)
	cfg.Bootstrap.Descriptor = writeFile(t, "proxies: 3\n")
	if _, err := openRuntime(ctx, cfg, slog.Default()); err == nil ||
		!strings.Contains(err.Error(), descriptor.ErrInvalid.Error()) {
		t.Fatalf("invalid descriptor: %v", err)
	}
}
