package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUnitServerTestOwnershipInventory(t *testing.T) {
	inventory, err := loadTestOwnership()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean("../..")
	seenPackages, seenFiles := map[string]bool{}, map[string]bool{}
	// Every scanned root, not a fixed list, so a staged directory that
	// ownership.OwnedTestDir accepts is checked from the commit that creates it.
	for _, area := range scanRoots {
		base := filepath.Join(root, area)
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(
			base,
			func(file string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					if entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") {
						return filepath.SkipDir
					}
					return nil
				}
				rel, err := filepath.Rel(root, file)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if !isServerTest(rel) {
					return nil
				}
				if _, ok := serverTestOwner(rel); !ok {
					t.Errorf("unclassified server test %s", rel)
				}
				seenPackages[path.Dir(rel)] = true
				seenFiles[rel] = true
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
	for dir, files := range inventory.SharedFiles {
		for _, file := range files {
			if !seenFiles[dir+"/"+file] {
				t.Errorf("inventory lists a missing shared test: %s/%s", dir, file)
			}
		}
	}
	for owner, files := range inventory.FormatDependencies {
		for _, file := range files {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
				t.Errorf("format dependency %s for %s: %v", file, owner, err)
			}
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
		{"new format test", change{status: "A", path: "test/server/artifacts/npm/new_test.go"}, "New, removed or moved"},
		{"unknown root test", change{status: "M", path: "internal/server/unknown_test.go"}, "Unmapped"},
		{"unknown package", change{status: "M", path: "test/server/newformat/routes_test.go"}, "Unmapped"},
		{"moved test", change{status: "R100", old: "internal/server/status_test.go", path: "test/server/artifacts/npm/status_test.go"}, "New, removed or moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify([]change{tc.change})
			if got.profile != "full" || !strings.Contains(got.reason, tc.reason) {
				t.Fatalf("plan = %+v", got)
			}
		})
	}
	if owner, ok := serverTestOwner(
		"test/server/artifacts/agentpackages/agentpackages_test.go",
	); !ok ||
		owner != "agentpackages" {
		t.Fatalf("agent package ownership = %q, %v", owner, ok)
	}
	if owner, ok := serverTestOwner("internal/server/status_test.go"); !ok || owner != "shared" {
		t.Fatalf("root status ownership = %q, %v", owner, ok)
	}
}

func TestUnitOwnershipV2RejectsInvalidInventories(t *testing.T) {
	valid := func() testOwnership {
		return testOwnership{
			Version: 2,
			Packages: map[string]string{
				"internal/server":                        "shared",
				"internal/server/xyz":                    "npm",
				"migration/access/adapters/http":         "shared",
				"migration/app":                          "shared",
				"migration/artifacts/ecosystem/npm/http": "npm",
				"test/server/artifacts/npm":              "npm",
				"test/server/artifacts/widget":           "widget",
			},
			SharedFiles: map[string][]string{
				"internal/server": {"npm_auth_paths_test.go", "status_test.go"},
				"migration/app":   {"router_test.go"},
			},
			FormatDependencies: map[string][]string{
				"npm": {"internal/server/npm_auth_paths_test.go", "migration/app/router_test.go"},
				// A non-shared package owner may hold dependencies before the
				// formats map knows it.
				"widget": {"migration/app/router_test.go"},
			},
		}
	}
	encode := func(t *testing.T, inventory testOwnership) []byte {
		t.Helper()
		data, err := json.Marshal(inventory)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, err := parseTestOwnership(encode(t, valid())); err != nil {
		t.Fatalf("valid inventory rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*testOwnership)
		want   string
	}{
		{"version 1", func(o *testOwnership) { o.Version = 1 }, "version 1, want 2"},
		{"missing version", func(o *testOwnership) { o.Version = 0 }, "version 0, want 2"},
		{"package outside owned directories", func(o *testOwnership) {
			o.Packages["internal/legacy/xyz"] = "npm"
		}, "invalid test package owner"},
		{"staged ecosystem package outside http", func(o *testOwnership) {
			o.Packages["migration/artifacts/ecosystem/npm"] = "npm"
		}, "invalid test package owner"},
		{"app subdirectory", func(o *testOwnership) {
			o.Packages["migration/app/apptest"] = "shared"
		}, "invalid test package owner"},
		{"unclean package key", func(o *testOwnership) {
			o.Packages["test/server/artifacts/npm/"] = "npm"
		}, "invalid test package owner"},
		{"empty owner", func(o *testOwnership) {
			o.Packages["test/server/artifacts/npm"] = ""
		}, "invalid test package owner"},
		{"shared files without a package", func(o *testOwnership) {
			o.SharedFiles["internal/app"] = []string{"app_test.go"}
		}, "invalid shared test directory"},
		{"shared files in a format package", func(o *testOwnership) {
			o.SharedFiles["test/server/artifacts/npm"] = []string{"publish_test.go"}
		}, "invalid shared test directory"},
		{"empty shared files", func(o *testOwnership) {
			o.SharedFiles["migration/access/adapters/http"] = []string{}
		}, "invalid shared test directory"},
		{"shared file with a directory", func(o *testOwnership) {
			o.SharedFiles["internal/server"] = append(o.SharedFiles["internal/server"], "npm/x_test.go")
		}, "invalid shared test file"},
		{"shared file that is not a test", func(o *testOwnership) {
			o.SharedFiles["internal/server"] = append(o.SharedFiles["internal/server"], "server.go")
		}, "invalid shared test file"},
		{"empty shared file name", func(o *testOwnership) {
			o.SharedFiles["internal/server"] = append(o.SharedFiles["internal/server"], "")
		}, "invalid shared test file"},
		{"duplicate shared file", func(o *testOwnership) {
			o.SharedFiles["internal/server"] = append(o.SharedFiles["internal/server"], "status_test.go")
		}, "invalid shared test file"},
		{"shared dependency owner", func(o *testOwnership) {
			o.FormatDependencies["shared"] = []string{"internal/server/status_test.go"}
		}, "invalid format dependency owner"},
		{"unknown dependency owner", func(o *testOwnership) {
			o.FormatDependencies["newformat"] = []string{"internal/server/status_test.go"}
		}, "invalid format dependency owner"},
		{"empty dependencies", func(o *testOwnership) {
			o.FormatDependencies["npm"] = []string{}
		}, "invalid format dependency owner"},
		{"dependency in a format package", func(o *testOwnership) {
			o.FormatDependencies["npm"] = []string{"test/server/artifacts/npm/publish_test.go"}
		}, "is not a listed shared test"},
		{"unlisted dependency", func(o *testOwnership) {
			o.FormatDependencies["npm"] = []string{"internal/server/unknown_test.go"}
		}, "is not a listed shared test"},
		{"bare dependency name", func(o *testOwnership) {
			o.FormatDependencies["npm"] = []string{"npm_auth_paths_test.go"}
		}, "is not a listed shared test"},
		{"duplicate dependency", func(o *testOwnership) {
			o.FormatDependencies["npm"] = []string{
				"internal/server/npm_auth_paths_test.go",
				"internal/server/npm_auth_paths_test.go",
			}
		}, "duplicate format dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory := valid()
			tc.mutate(&inventory)
			_, err := parseTestOwnership(encode(t, inventory))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	for name, data := range map[string]string{
		"version 1 file": `{"version": 1, "packages": {"internal/server": "shared"},
			"root_files": ["status_test.go"], "root_format_dependencies": {},
			"app_format_dependencies": {}}`,
		"version 1 key beside version 2": `{"version": 2, "packages": {}, "root_files": []}`,
		"trailing data":                  `{"version": 2, "packages": {}} {}`,
		"malformed":                      `{"version": 2,`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTestOwnership([]byte(data)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// useTestOwnership makes the planner read inventory until the test ends.
func useTestOwnership(t *testing.T, inventory testOwnership) {
	t.Helper()
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTestOwnership(data)
	if err != nil {
		t.Fatalf("synthetic inventory is invalid: %v", err)
	}
	saved := loadTestOwnership
	loadTestOwnership = func() (testOwnership, error) { return parsed, nil }
	t.Cleanup(func() { loadTestOwnership = saved })
}

func TestUnitSharedFilesFollowRootTestMoves(t *testing.T) {
	root := t.TempDir()
	for file, source := range map[string]string{
		"internal/server/status_test.go":                    "package server\nfunc TestAccStatus() {}\n",
		"internal/server/registry_shared_test.go":           "package server\nfunc TestAccRegistryShared() {}\n",
		"migration/artifacts/ecosystem/npm/tarball_test.go": "package npm\nfunc TestUnitTarball() {}\n",
		"test/server/artifacts/npm/npm_auth_paths_test.go": "package npm\n" +
			"func TestUnitNPMAuthPaths() {}\nfunc TestAccNPMAuthPaths() {}\n",
	} {
		target := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	useTestOwnership(t, testOwnership{
		Version:  2,
		Packages: map[string]string{"internal/server": "shared"},
		SharedFiles: map[string][]string{"internal/server": {
			"npm_auth_paths_test.go", "registry_shared_test.go", "status_test.go",
		}},
		FormatDependencies: map[string][]string{
			"docker": {"internal/server/registry_shared_test.go"},
			"npm":    {"internal/server/npm_auth_paths_test.go"},
		},
	})
	owner, ok := serverTestOwner("internal/server/npm_auth_paths_test.go")
	if !ok || owner != "shared" {
		t.Fatalf("root test before the move = %q, %t", owner, ok)
	}
	before := proposeAffected(
		[]change{{status: "M", path: "migration/artifacts/ecosystem/npm/tarball.go"}},
	)
	if !before.hasSharedTests || !slices.Contains(before.units, "./internal/server") {
		t.Fatalf("npm plan before the move omitted its root dependency: %+v", before)
	}
	// The move: the name leaves shared_files, its directory gains a package
	// key and owner, and the dependency path that named it is dropped.
	useTestOwnership(t, testOwnership{
		Version: 2,
		Packages: map[string]string{
			"internal/server":           "shared",
			"test/server/artifacts/npm": "npm",
		},
		SharedFiles: map[string][]string{"internal/server": {
			"registry_shared_test.go", "status_test.go",
		}},
		FormatDependencies: map[string][]string{
			"docker": {"internal/server/registry_shared_test.go"},
		},
	})
	for file, want := range map[string]string{
		"test/server/artifacts/npm/npm_auth_paths_test.go": "npm",
		"internal/server/status_test.go":                   "shared",
		"internal/server/registry_shared_test.go":          "shared",
	} {
		if owner, ok := serverTestOwner(file); !ok || owner != want {
			t.Errorf("%s owner = %q, %t; want %q", file, owner, ok, want)
		}
	}
	if _, ok := serverTestOwner("internal/server/npm_auth_paths_test.go"); ok {
		t.Error("a test recreated under its old root name is classified without review")
	}
	for _, c := range []change{
		{status: "M", path: "internal/server/npm_auth_paths_test.go"},
		{status: "A", path: "test/server/artifacts/npm/new_test.go"},
		{status: "R100", old: "internal/server/status_test.go", path: "test/server/artifacts/npm/status_test.go"},
	} {
		if got := classify([]change{c}); got.profile != "full" {
			t.Errorf("%+v selected %+v", c, got)
		}
		if got := proposeAffected([]change{c}); got.profile != "full" {
			t.Errorf("%+v proposed %+v", c, got)
		}
	}
	moved := change{status: "M", path: "test/server/artifacts/npm/npm_auth_paths_test.go"}
	npm := proposeAffected([]change{moved})
	if npm.profile != "format" || npm.owner != "npm" || npm.hasSharedTests ||
		!slices.Contains(npm.units, "./test/server/artifacts/npm") ||
		slices.Contains(npm.units, "./internal/server") {
		t.Fatalf("npm plan after the move: %+v", npm)
	}
	selection, err := scanKinds(root, plan{profile: "format", candidate: npm})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range selection.Selected {
		names = append(names, entry.Package+" "+entry.Name)
	}
	if want := []string{
		"./migration/artifacts/ecosystem/npm TestUnitTarball",
		"./test/server/artifacts/npm TestAccNPMAuthPaths",
		"./test/server/artifacts/npm TestUnitNPMAuthPaths",
	}; !slices.Equal(names, want) {
		t.Fatalf("npm selection after the move = %q, want %q", names, want)
	}
	docker := proposeAffected(
		[]change{{status: "M", path: "migration/artifacts/ecosystem/docker/handler.go"}},
	)
	selection, err = scanKinds(root, plan{profile: "format", candidate: docker})
	if err != nil {
		t.Fatal(err)
	}
	if !docker.hasSharedTests || len(selection.Selected) != 1 ||
		selection.Selected[0].Source != "internal/server/registry_shared_test.go" {
		t.Fatalf("the remaining root dependency is not selected alone: %+v, %+v", docker, selection)
	}
}
