package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putTestSource(t *testing.T, root, path, source string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUnitScanClassifiesMixedPackageAcrossBuildTags(t *testing.T) {
	root := t.TempDir()
	putTestSource(t, root, "internal/mixed/mixed_test.go", `package mixed
import "testing"
func TestUnitParser(t *testing.T) {}
func TestAccAPI(t *testing.T) {}
`)
	putTestSource(t, root, "internal/mixed/windows_only_windows_test.go", `//go:build windows
package mixed
import "testing"
func TestUnitWindows(t *testing.T) {}
`)
	putTestSource(t, root, "internal/mixed/testdata/fixture_test.go", `package fixture
func TestFixture() {}
`)
	got, err := scan(root, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unit) != 2 || len(got.Acc) != 1 || got.Unit[1].Name != "TestUnitWindows" {
		t.Fatalf("mixed and platform tests: %+v", got)
	}
}

func TestUnitScanRejectsUnclassifiedPlatformTest(t *testing.T) {
	root := t.TempDir()
	putTestSource(t, root, "pkg/example/example_linux_test.go", `//go:build linux
package example
import "testing"
func TestLegacy(t *testing.T) {}
func TestAccess(t *testing.T) {}
`)
	_, err := scan(root, map[string]string{}, map[string]string{})
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"pkg/example/example_linux_test.go:TestLegacy: test kind must be TestUnit or TestAcc",
		) ||
		!strings.Contains(
			err.Error(),
			"pkg/example/example_linux_test.go:TestAccess: test kind must be TestUnit or TestAcc",
		) {
		t.Fatalf("missing expected classification failure: %v", err)
	}
}

func TestUnitScanInventoriesOwnedGoExceptions(t *testing.T) {
	root := t.TempDir()
	putTestSource(t, root, "test/example/example_test.go", `package example
import "testing"
func TestMain(m *testing.M) {}
func FuzzInput(f *testing.F) {}
`)
	mains := map[string]string{"test/example/example_test.go:TestMain": "sample suite"}
	fuzz := map[string]string{"test/example/example_test.go:FuzzInput": "sample fuzz owner"}
	got, err := scan(root, mains, fuzz)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TestMain) != 1 || len(got.Fuzz) != 1 || got.Fuzz[0].Owner != "sample fuzz owner" {
		t.Fatalf("exceptions: %+v", got)
	}
	_, err = scan(root, mains, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "unowned fuzz target") {
		t.Fatalf("missing unowned fuzz failure: %v", err)
	}
}
