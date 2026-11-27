package clienttest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
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

func TestUnitMavenToolsRejectAnythingButPinnedTools(t *testing.T) {
	allowed := []string{"org/apache/tool/1/tool-1.jar", "org/apache/tool/1/tool-1.pom"}
	seed := func(t *testing.T, names ...string) string {
		t.Helper()
		repository := t.TempDir()
		for _, name := range names {
			path := filepath.Join(repository, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return repository
	}
	if err := CheckMavenToolRepository(seed(t, allowed...), allowed); err != nil {
		t.Fatal(err)
	}
	for name, names := range map[string][]string{
		"product jar":      append(slices.Clone(allowed), "org/acme/smoke/lib/1.0.0/lib-1.0.0.jar"),
		"new fixture":      append(slices.Clone(allowed), "com/example/app/1/app-1.pom"),
		"resolver records": append(slices.Clone(allowed), "org/apache/tool/1/_remote.repositories"),
		"incomplete":       allowed[:1],
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckMavenToolRepository(seed(t, names...), allowed); err == nil {
				t.Fatal("client accepted a tool tail that is not exactly the pinned tools")
			}
		})
	}
	if err := CheckMavenToolRepository(seed(t, allowed...), nil); err == nil {
		t.Fatal("an unreadable manifest allowed a tool tail")
	}
}

func TestUnitMavenCommittedManifestNamesTools(t *testing.T) {
	names := MavenToolLock(repopath.Root(t))
	if len(names) == 0 {
		t.Fatal("committed Maven tool manifest is unreadable or empty")
	}
	for _, name := range names {
		if strings.HasPrefix(name, "org/acme/") || strings.HasPrefix(name, "org/weave/") {
			t.Errorf("manifest pins product coordinate %s", name)
		}
	}
}
