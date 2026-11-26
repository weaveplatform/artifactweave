package peers

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUnitCollectCoverage(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	children := []string{filepath.Join(parent, "first"), filepath.Join(parent, "second")}
	for i, child := range children {
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]string{"covmeta.shared": "identical", "covcounters.shared." + strconv.Itoa(i+1): "counters"} {
			if err := os.WriteFile(filepath.Join(child, name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	results := make(chan error, len(children))
	for _, child := range children {
		go func() { results <- collectCoverage(parent, child) }()
	}
	for range children {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 3 {
		t.Fatalf("collected %v (%v), want one metadata file and both counters", entries, err)
	}
	for _, child := range children {
		if _, err := os.Stat(child); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("collected directory remains: %v", err)
		}
	}
}

func TestUnitCollectCoverageRefusesDataLoss(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, file, existing, child, want string }{
		{"different metadata", "covmeta.shared", "original", "different", "metadata differs"},
		{"duplicate counter", "covcounters.shared.1", "original", "different", "counter already exists"},
		{"incomplete metadata", "tmp.covmeta.shared", "", "incomplete", "unexpected child coverage entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			child := filepath.Join(parent, "child")
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			source, target := filepath.Join(child, tc.file), filepath.Join(parent, tc.file)
			if err := os.WriteFile(source, []byte(tc.child), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.existing != "" {
				if err := os.WriteFile(target, []byte(tc.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := collectCoverage(parent, child); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("collect error %v, want %q", err, tc.want)
			}
			if data, err := os.ReadFile(source); err != nil || string(data) != tc.child {
				t.Fatalf("lost uncollected child data %q (%v)", data, err)
			}
			if tc.existing != "" {
				if data, err := os.ReadFile(target); err != nil || string(data) != tc.existing {
					t.Fatalf("overwrote collected data %q (%v)", data, err)
				}
			}
		})
	}
}
