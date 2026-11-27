package clienttest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnitMavenManifestRefusesMissingAndMalformed(t *testing.T) {
	root := t.TempDir()
	if got := MavenToolLock(root); got != nil {
		t.Fatal("missing manifest supplied tools")
	}
	dir := filepath.Join(root, "test", "acceptance", "clients")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"invalid", `{"repository":{}}`} {
		if err := os.WriteFile(filepath.Join(dir, "maven-tools.lock.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := MavenToolLock(root); len(got) != 0 {
			t.Fatal("invalid manifest supplied tools")
		}
	}
	if err := CheckMavenToolRepository(filepath.Join(root, "absent"), []string{"tool.jar"}); err == nil {
		t.Fatal("missing tool supply accepted")
	}
}

func TestUnitMavenSeedReportsFailedPreparation(t *testing.T) {
	t.Setenv("ARTIFACTWEAVE_MAVEN_TOOLS", t.TempDir())
	if err := SeedMavenTools(t.TempDir(), "--repository", t.TempDir()); err == nil {
		t.Fatal("missing seed script accepted")
	}
}
