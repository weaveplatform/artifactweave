package huggingface

import "testing"

func TestUnitSnapshotIdentityAndPath(t *testing.T) {
	id, ok := ParseIdentity("models", "acme", "demo_1")
	if !ok {
		t.Fatal("valid model rejected")
	}
	rev := "0123456789012345678901234567890123456789"
	p, ok := SnapshotPath(id, rev, "nested/é.txt")
	if !ok {
		t.Fatal("valid UTF-8 file rejected")
	}
	got, gotRev, file, ok := ParseSnapshotPath(p)
	if !ok || got != id || gotRev != rev || file != "nested/é.txt" {
		t.Fatalf("roundtrip %v %q %q %v", got, gotRev, file, ok)
	}
	if dataset, ok := ParseIdentity(
		"datasets",
		"acme",
		"demo_1",
	); !ok ||
		dataset.Prefix() == id.Prefix() {
		t.Fatal("dataset identity collided")
	}
}

func TestUnitRejectTraversalAndLegacyLayout(t *testing.T) {
	rev := "0123456789012345678901234567890123456789"
	for _, file := range []string{"../private", "a/../b", "/absolute", "a\\b", "a//b", "a/./b", "nul\x00byte", "a/"} {
		if FilePath(file) {
			t.Errorf("accepted %q", file)
		}
	}
	for _, path := range []string{"acme/demo/snapshots/" + rev + "/x", "models--acme--demo/refs/main/x", "models--acme--demo/snapshots/../x"} {
		if _, _, _, ok := ParseSnapshotPath(path); ok {
			t.Errorf("accepted %q", path)
		}
	}
	for _, owner := range []string{"-acme", "a--b", "../x", "123"} {
		if _, ok := ParseIdentity("models", owner, "demo"); ok {
			t.Errorf("accepted owner %q", owner)
		}
	}
}

func TestUnitNamedRevisionRefPath(t *testing.T) {
	id, _ := ParseIdentity("models", "acme", "demo")
	for _, name := range []string{"main", "v1", "release.2"} {
		path, ok := RefPath(id, name)
		if !ok {
			t.Fatalf("safe revision %q rejected", name)
		}
		got, ref, parsed := ParseRefPath(path)
		if !parsed || got != id || ref != name {
			t.Fatalf("ref roundtrip %q", path)
		}
	}
	for _, name := range []string{"../x", "a--b", "a/b", "a^b", ""} {
		if _, ok := RefPath(id, name); ok {
			t.Errorf("unsafe ref %q accepted", name)
		}
	}
}

func TestUnitMalformedModelNamesAndLegacyPrefixesStayOutsideCurrentLayout(t *testing.T) {
	for _, name := range []string{".hidden", "bad/name"} {
		if _, ok := ParseIdentity("models", "acme", name); ok {
			t.Errorf("accepted unsafe model name %q", name)
		}
	}
	if _, _, ok := ParseRefPath("legacy/refs/main"); ok {
		t.Fatal("legacy prefix accepted as a current revision ref")
	}
	if _, _, _, ok := ParseSnapshotPath(
		"legacy/snapshots/0123456789012345678901234567890123456789/config.json",
	); ok {
		t.Fatal("legacy prefix accepted as a current snapshot")
	}
}
