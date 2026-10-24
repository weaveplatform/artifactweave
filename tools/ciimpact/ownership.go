package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
)

// test_ownership.json is a package boundary, not a selector of test names.
// A new root test or test package stays on the full profile until its owner
// is reviewed and recorded here.
//
//go:embed test_ownership.json
var testOwnershipJSON []byte

type testOwnership struct {
	Version                int                 `json:"version"`
	Packages               map[string]string   `json:"packages"`
	RootFiles              []string            `json:"root_files"`
	RootFormatDependencies map[string][]string `json:"root_format_dependencies"`
}

var (
	testOwnershipOnce sync.Once
	testOwnershipData testOwnership
	testOwnershipErr  error
)

func loadTestOwnership() (testOwnership, error) {
	testOwnershipOnce.Do(func() {
		testOwnershipErr = json.Unmarshal(testOwnershipJSON, &testOwnershipData)
		if testOwnershipErr != nil {
			return
		}
		if testOwnershipData.Version != 1 ||
			testOwnershipData.Packages["internal/server"] != "shared" {
			testOwnershipErr = fmt.Errorf("invalid server test ownership inventory")
			return
		}
		seen := map[string]bool{}
		for _, file := range testOwnershipData.RootFiles {
			if file == "" || strings.Contains(file, "/") || !strings.HasSuffix(file, "_test.go") ||
				seen[file] {
				testOwnershipErr = fmt.Errorf("invalid root test file %q", file)
				return
			}
			seen[file] = true
		}
		for pkg, owner := range testOwnershipData.Packages {
			if !(pkg == "internal/server" || strings.HasPrefix(pkg, "internal/server/") ||
				strings.HasPrefix(pkg, "test/server/")) || owner == "" {
				testOwnershipErr = fmt.Errorf("invalid test package owner %q: %q", pkg, owner)
				return
			}
		}
		owners := map[string]bool{}
		for _, owner := range testOwnershipData.Packages {
			owners[owner] = true
		}
		for owner, files := range testOwnershipData.RootFormatDependencies {
			_, knownFormat := formats[owner]
			if owner == "shared" || !owners[owner] && !knownFormat || len(files) == 0 {
				testOwnershipErr = fmt.Errorf("invalid root format dependency %q", owner)
				return
			}
			for _, file := range files {
				if !seen[file] {
					testOwnershipErr = fmt.Errorf("unknown root dependency %q for %s", file, owner)
					return
				}
			}
		}
	})
	return testOwnershipData, testOwnershipErr
}

func serverTestOwner(file string) (string, bool) {
	if !strings.HasSuffix(file, "_test.go") {
		return "", false
	}
	inventory, err := loadTestOwnership()
	if err != nil {
		return "", false
	}
	dir := path.Dir(file)
	owner, ok := inventory.Packages[dir]
	if !ok {
		return "", false
	}
	if dir == "internal/server" {
		for _, known := range inventory.RootFiles {
			if known == path.Base(file) {
				return owner, true
			}
		}
		return "", false
	}
	return owner, true
}
