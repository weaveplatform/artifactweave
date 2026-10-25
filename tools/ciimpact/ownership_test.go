package main

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitServerTestOwnershipInventory(t *testing.T) {
	inventory, err := loadTestOwnership()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean("../..")
	seenPackages, seenRoot := map[string]bool{}, map[string]bool{}
	for _, dir := range []string{"internal/server", "test/server"} {
		err := filepath.WalkDir(
			filepath.Join(root, dir),
			func(file string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !strings.HasSuffix(file, "_test.go") {
					return nil
				}
				rel, err := filepath.Rel(root, file)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if _, ok := serverTestOwner(rel); !ok {
					t.Errorf("unclassified server test %s", rel)
				}
				pkg := filepath.ToSlash(filepath.Dir(rel))
				seenPackages[pkg] = true
				if pkg == "internal/server" {
					seenRoot[filepath.Base(rel)] = true
				}
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	for pkg := range inventory.Packages {
		if !seenPackages[pkg] {
			t.Errorf("inventory lists a package without tests: %s", pkg)
		}
	}
	for _, file := range inventory.RootFiles {
		if !seenRoot[file] {
			t.Errorf("inventory lists a missing root test: %s", file)
		}
	}
}

func TestUnitServerTestChangesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change change
		reason string
	}{
		{"new root test", change{status: "A", path: "internal/server/new_test.go"}, "New, removed or moved"},
		{"new format test", change{status: "A", path: "test/server/npm/new_test.go"}, "New, removed or moved"},
		{"unknown root test", change{status: "M", path: "internal/server/unknown_test.go"}, "Unmapped"},
		{"unknown package", change{status: "M", path: "test/server/newformat/routes_test.go"}, "Unmapped"},
		{"moved test", change{status: "R100", old: "internal/server/status_test.go", path: "test/server/npm/status_test.go"}, "New, removed or moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify([]change{tc.change})
			if got.profile != "full" || !strings.Contains(got.reason, tc.reason) {
				t.Fatalf("plan = %+v", got)
			}
		})
	}
	if owner, ok := serverTestOwner(
		"test/server/agentpackages/agentpackages_test.go",
	); !ok ||
		owner != "agentpackages" {
		t.Fatalf("agent package ownership = %q, %v", owner, ok)
	}
	if owner, ok := serverTestOwner("internal/server/status_test.go"); !ok || owner != "shared" {
		t.Fatalf("root status ownership = %q, %v", owner, ok)
	}
}
