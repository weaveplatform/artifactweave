package specoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func snapshotFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	writeCache(t, dir, "current", "page.json", theirRepoBlock)
	sum := sha256.Sum256([]byte(theirRepoBlock))
	entry := map[string]any{
		"family": "current", "index": "https://docs.example/current/llms.txt",
		"url": "https://docs.example/current/page.md", "file": "current/page.json",
		"sha256": hex.EncodeToString(sum[:]),
	}
	return dir, entry
}

func writeSnapshot(t *testing.T, dir string, entry map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"families": []map[string]any{{"family": "current", "index": "https://docs.example/current/llms.txt"}},
		"entries":  []map[string]any{entry},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnitManifestSnapshotIgnoresOrphanFiles(t *testing.T) {
	dir, entry := snapshotFixture(t)
	writeSnapshot(t, dir, entry)
	writeCache(t, dir, "current", "broken-orphan.json", "not json")
	writeCache(t, dir, "retired", "old.json", `{"paths":{"/retired":{"get":{"responses":{"200":{}}}}}}`)
	got, err := Load(dir)
	if err != nil || len(got.Errors) != 0 || len(got.Ops) != 2 {
		t.Fatalf("only the declared current snapshot should load: ops=%d errors=%v err=%v", len(got.Ops), got.Errors, err)
	}
	inv, err := BuildInventory(dir)
	if err != nil || inv.Pages != 1 || len(inv.Errors) != 0 {
		t.Fatalf("inventory must use the same declared snapshot: pages=%d errors=%v err=%v", inv.Pages, inv.Errors, err)
	}
	if err := CheckFamilies(dir); err != nil {
		t.Fatalf("valid declared snapshot: %v", err)
	}
}

func TestUnitManifestSnapshotRejectsRecordedDamage(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string, map[string]any)
	}{
		{"missing", func(t *testing.T, dir string, _ map[string]any) {
			t.Helper()
			if err := os.Remove(filepath.Join(dir, "current", "page.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, dir string, e map[string]any) {
			t.Helper()
			writeCache(t, dir, "current", "page.json", "not json")
			delete(e, "sha256")
		}},
		{"checksum mismatch", func(_ *testing.T, _ string, e map[string]any) { e["sha256"] = "different" }},
		{"traversal", func(_ *testing.T, _ string, e map[string]any) { e["file"] = "../outside.json" }},
		{"absolute", func(_ *testing.T, _ string, e map[string]any) { e["file"] = "/outside.json" }},
		{"windows path", func(_ *testing.T, _ string, e map[string]any) { e["file"] = `current\page.json` }},
		{"wrong family", func(_ *testing.T, _ string, e map[string]any) { e["family"] = "other" }},
		{"directory in place of file", func(t *testing.T, dir string, _ map[string]any) {
			t.Helper()
			p := filepath.Join(dir, "current", "page.json")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"file symlink", func(t *testing.T, dir string, _ map[string]any) {
			t.Helper()
			target := filepath.Join(t.TempDir(), "private.json")
			if err := os.WriteFile(target, []byte(theirRepoBlock), 0o600); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "current", "page.json")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, p); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, entry := snapshotFixture(t)
			tc.change(t, dir, entry)
			writeSnapshot(t, dir, entry)
			if _, err := Load(dir); err == nil {
				t.Error("Load accepted damaged declared snapshot")
			}
			if _, err := BuildInventory(dir); err == nil {
				t.Error("inventory accepted damaged declared snapshot")
			}
			if err := CheckFamilies(dir); err == nil {
				t.Error("family guard accepted damaged declared snapshot")
			}
		})
	}
}
