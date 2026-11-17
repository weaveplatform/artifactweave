package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUnitMavenSnapshotCleanupInterval covers maven.snapshot_cleanup_interval
// (#283): 7.x's five minutes by default, the file, environment and flag
// forms in their precedence, and the refusal of less than a second.
func TestUnitMavenSnapshotCleanupInterval(t *testing.T) {
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

func TestUnitMavenMetadataVersion3(t *testing.T) {
	if Default().Maven.MetadataVersion3Enabled != nil {
		t.Fatal("default must enable M3 metadata")
	}
	file := filepath.Join(t.TempDir(), "maven.yaml")
	if err := os.WriteFile(file, []byte("maven:\n  metadata_version3_enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.Maven.MetadataVersion3Enabled == nil || *cfg.Maven.MetadataVersion3Enabled {
		t.Fatal(cfg.Maven, err)
	}
	if got := cfg.Artifacts().Formats.Maven.MetadataVersion3Enabled; got == nil || *got {
		t.Fatal("setting lost at composition")
	}
	cfg, err = Load(LoadOptions{File: file, Env: func(k string) (string, bool) { return "true", k == "ARTIFACTWEAVE_MAVEN_METADATA_VERSION3_ENABLED" }})
	if err != nil || !*cfg.Maven.MetadataVersion3Enabled {
		t.Fatal(cfg.Maven, err)
	}
	cfg, err = Load(LoadOptions{Flags: map[string]string{"maven.metadata_version3_enabled": "false"}})
	if err != nil || *cfg.Maven.MetadataVersion3Enabled {
		t.Fatal(cfg.Maven, err)
	}
	if _, err := Load(LoadOptions{Flags: map[string]string{"maven.metadata_version3_enabled": "bad"}}); !errors.Is(err, ErrInvalidValue) {
		t.Fatal(err)
	}
}
