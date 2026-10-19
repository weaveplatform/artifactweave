package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMavenSnapshotCleanupInterval covers maven.snapshot_cleanup_interval
// (#283): 7.x's five minutes by default, the file, environment and flag
// forms in their precedence, and the refusal of less than a second.
func TestMavenSnapshotCleanupInterval(t *testing.T) {
	if d := Default().Maven.SnapshotCleanupInterval; d != 5*time.Minute {
		t.Fatalf("default %s", d)
	}
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(
		file, []byte("maven:\n  snapshot_cleanup_interval: 1m\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.Maven.SnapshotCleanupInterval != time.Minute {
		t.Fatalf("file: %+v, %v", cfg.Maven, err)
	}
	env := map[string]string{"ARTIFACTWEAVE_MAVEN_SNAPSHOT_CLEANUP_INTERVAL": "2m"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	cfg, err = Load(LoadOptions{File: file, Env: lookup})
	if err != nil || cfg.Maven.SnapshotCleanupInterval != 2*time.Minute {
		t.Fatalf("env: %+v, %v", cfg.Maven, err)
	}
	cfg, err = Load(LoadOptions{
		File: file, Env: lookup,
		Flags: map[string]string{"maven.snapshot_cleanup_interval": "3m"},
	})
	if err != nil || cfg.Maven.SnapshotCleanupInterval != 3*time.Minute {
		t.Fatalf("flags: %+v, %v", cfg.Maven, err)
	}
	for value, want := range map[string]error{
		"500ms": ErrInvalidValue, "soon": ErrInvalidValue,
	} {
		_, err := Load(
			LoadOptions{Flags: map[string]string{"maven.snapshot_cleanup_interval": value}},
		)
		if !errors.Is(err, want) {
			t.Errorf("%s: %v", value, err)
		}
	}
	if _, err := Load(LoadOptions{Flags: map[string]string{"maven.nosuch": "1m"}}); !errors.Is(
		err, ErrUnknownKey,
	) {
		t.Errorf("maven.nosuch: %v", err)
	}
}
