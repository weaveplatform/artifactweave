package peers_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUnitColdImageConfigReadsLayoutDirectory(t *testing.T) {
	t.Parallel()
	layer := sha256.Sum256([]byte("owned fixture layer"))
	diffID := "sha256:" + hex.EncodeToString(layer[:])
	data := []byte(`{"rootfs":{"diff_ids":["` + diffID + `"]}}`)
	dir, digest := writeColdImageConfig(t, data)
	verifyColdImage(t, dir, digest, diffID)
}

func writeColdImageConfig(t *testing.T, data []byte) (string, string) {
	t.Helper()
	dir := t.TempDir()
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, digest), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, "sha256:" + digest
}

func TestUnitColdImageConfigRejectsInvalidProof(t *testing.T) {
	t.Parallel()
	layer := sha256.Sum256([]byte("owned fixture layer"))
	diffID := "sha256:" + hex.EncodeToString(layer[:])
	valid := `{"rootfs":{"diff_ids":["` + diffID + `"]}}`
	for _, tc := range []struct {
		name, body, mutate string
	}{
		{name: "missing blob", body: valid, mutate: "remove"},
		{name: "corrupt digest", body: valid, mutate: "corrupt"},
		{name: "malformed config", body: "{"},
		{name: "missing rootfs", body: "{}"},
		{name: "different rootfs", body: `{"rootfs":{"diff_ids":["sha256:` + strings.Repeat("0", 64) + `"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []byte(tc.body)
			dir, digest := writeColdImageConfig(t, data)
			path := filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
			switch tc.mutate {
			case "remove":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				// Valid JSON and the same rootfs, but different bytes at the
				// original content address: only the digest check catches it.
				if err := os.WriteFile(path, append(slices.Clone(data), ' '), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkColdImageConfig(dir, digest, diffID); err == nil {
				t.Fatal("invalid cold image proof was accepted")
			}
		})
	}
}
