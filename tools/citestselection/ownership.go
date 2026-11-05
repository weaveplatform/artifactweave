package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
)

// test_ownership.json is a package boundary, not a selector of test names.
// A new root test or test package stays on the full profile until its owner
// is reviewed and recorded here.
//
//go:embed test_ownership.json
var testOwnershipJSON []byte

// testOwnership is keyed by directory, so a test that leaves a shared
// directory is recorded by editing paths, never by a schema field for the
// directory it leaves or joins.
type testOwnership struct {
	Version int `json:"version"`
	// Packages maps each owned test directory to a format owner or "shared".
	Packages map[string]string `json:"packages"`
	// SharedFiles lists, per shared directory whose files are reviewed one by
	// one, the bare names of its test files; an unlisted file there is unmapped.
	SharedFiles map[string][]string `json:"shared_files"`
	// FormatDependencies maps a format to the repository paths of shared test
	// files that exercise it, so a narrow plan runs only those files there.
	FormatDependencies map[string][]string `json:"format_dependencies"`
}

// loadTestOwnership is a variable so tests can plan against a synthetic
// inventory; the selector itself reads only the embedded file.
var loadTestOwnership = sync.OnceValues(func() (testOwnership, error) {
	return parseTestOwnership(testOwnershipJSON)
})

// parseTestOwnership rejects unknown keys and trailing data because a v1 key
// left beside the v2 ones would otherwise be ignored, and the inventory would
// silently lose the facts it held.
func parseTestOwnership(data []byte) (testOwnership, error) {
	var inventory testOwnership
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return testOwnership{}, fmt.Errorf("decode server test ownership: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return testOwnership{}, fmt.Errorf("server test ownership has data after its object")
	}
	if err := inventory.validate(); err != nil {
		return testOwnership{}, err
	}
	return inventory, nil
}

func (o testOwnership) validate() error {
	if o.Version != 2 {
		return fmt.Errorf("server test ownership version %d, want 2", o.Version)
	}
	owners := map[string]bool{}
	for dir, owner := range o.Packages {
		if !ownedTestDir(dir) || owner == "" {
			return fmt.Errorf("invalid test package owner %q: %q", dir, owner)
		}
		if owner != "shared" {
			owners[owner] = true
		}
	}
	for dir, files := range o.SharedFiles {
		if o.Packages[dir] != "shared" || len(files) == 0 {
			return fmt.Errorf("invalid shared test directory %q", dir)
		}
		seen := map[string]bool{}
		for _, file := range files {
			if file == "" || strings.Contains(file, "/") || !strings.HasSuffix(file, "_test.go") ||
				seen[file] {
				return fmt.Errorf("invalid shared test file %q in %s", file, dir)
			}
			seen[file] = true
		}
	}
	for owner, files := range o.FormatDependencies {
		// A plan's owner is always a format, so an entry for "shared" could
		// never be read; it is neither kind of owner accepted here.
		if _, knownFormat := formats[owner]; (!knownFormat && !owners[owner]) || len(files) == 0 {
			return fmt.Errorf("invalid format dependency owner %q", owner)
		}
		seen := map[string]bool{}
		for _, file := range files {
			if seen[file] {
				return fmt.Errorf("duplicate format dependency %q of %s", file, owner)
			}
			if !slices.Contains(o.SharedFiles[path.Dir(file)], path.Base(file)) {
				return fmt.Errorf(
					"format dependency %q of %s is not a listed shared test",
					file,
					owner,
				)
			}
			seen[file] = true
		}
	}
	return nil
}

// ownedTestDir reports whether the tests in dir need a reviewed owner: server
// assembly tests, the application tests and the HTTP adapter tests. P9d builds
// the target tree under migration/ and then renames it to internal/ (decision
// 0044), so each staged pattern accepts both roots; internal/app is the
// internal form of <root>/app.
func ownedTestDir(dir string) bool {
	// An unclean inventory key such as "test/server/npm/" could never equal
	// a test file's directory, so its tests would stay unmapped.
	if dir != path.Clean(dir) {
		return false
	}
	if dir == "internal/server" || strings.HasPrefix(dir, "internal/server/") ||
		dir == "test/server" || strings.HasPrefix(dir, "test/server/") {
		return true
	}
	parts := strings.Split(dir, "/")
	if parts[0] != "migration" && parts[0] != "internal" {
		return false
	}
	switch {
	case len(parts) == 2:
		return parts[1] == "app"
	case len(parts) >= 5 && parts[1] == "artifacts" && parts[2] == "ecosystem":
		return parts[4] == "http"
	case len(parts) >= 4:
		return parts[2] == "adapters" && parts[3] == "http"
	}
	return false
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
	if files, shared := inventory.SharedFiles[dir]; shared &&
		!slices.Contains(files, path.Base(file)) {
		return "", false
	}
	return owner, true
}
