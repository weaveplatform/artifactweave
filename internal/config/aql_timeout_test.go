package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUnitAQLTimeout covers aql_timeout (#411): Artifactory's defaults, the
// file, environment and flag forms, and the refusal of a negative value.
func TestUnitAQLTimeout(t *testing.T) {
	if d := Default().AQLTimeout; d.Query != 900*time.Second || d.Streamed != 7200*time.Second {
		t.Fatalf("defaults %+v", d)
	}
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(
		file,
		[]byte("aql_timeout:\n  query: 30s\n  streamed: 1m\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{File: file})
	if err != nil ||
		cfg.AQLTimeout != (AQLTimeoutConfig{Query: 30 * time.Second, Streamed: time.Minute}) {
		t.Fatalf("file: %+v, %v", cfg.AQLTimeout, err)
	}
	env := map[string]string{"ARTIFACTWEAVE_AQL_TIMEOUT_QUERY": "0s"}
	cfg, err = Load(LoadOptions{
		File:  file,
		Env:   func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Flags: map[string]string{"aql_timeout.streamed": "3h"},
	})
	if err != nil || cfg.AQLTimeout != (AQLTimeoutConfig{Streamed: 3 * time.Hour}) {
		t.Fatalf("env and flags: %+v, %v", cfg.AQLTimeout, err)
	}
	for key, value := range map[string]string{
		"aql_timeout.query": "-1s", "aql_timeout.streamed": "-1s",
	} {
		_, err := Load(LoadOptions{Flags: map[string]string{key: value}})
		if !errors.Is(err, ErrInvalidValue) {
			t.Errorf("%s=%s: %v", key, value, err)
		}
	}
	for key, want := range map[string]error{
		"aql_timeout.query": ErrInvalidValue, "aql_timeout.nosuch": ErrUnknownKey,
	} {
		_, err := Load(LoadOptions{Flags: map[string]string{key: "soon"}})
		if !errors.Is(err, want) {
			t.Errorf("%s=soon: %v", key, err)
		}
	}
}
