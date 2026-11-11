package repopath

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ErrNoModule reports that no directory at or above the starting directory
// holds a go.mod file.
var ErrNoModule = errors.New("no go.mod at or above the directory")

// FindRoot returns the nearest directory at or above dir, dir included, that
// holds a go.mod file. A relative dir is first made absolute against the
// working directory, so the search can climb above it. The error wraps
// ErrNoModule when the search reaches the file system root without a match.
func FindRoot(dir string) (string, error) {
	// Climbing stops on the first error, so a failure to make dir absolute
	// and a search that runs out of parents leave through the same return.
	d, err := filepath.Abs(dir)
	for err == nil {
		if _, statErr := os.Stat(filepath.Join(d, "go.mod")); statErr == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			err = ErrNoModule
		}
		d = parent
	}
	return "", fmt.Errorf("find the module root from %s: %w", dir, err)
}

// Root returns the repository root: FindRoot of the working directory, which
// go test sets to the directory of the package under test. It fails the test
// when there is no root.
func Root(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err == nil {
		dir, err = FindRoot(dir)
	}
	if err != nil {
		tb.Fatal(err)
	}
	return dir
}

// Path returns the absolute path of rel, a slash-separated path relative to
// the repository root such as "api/access.openapi.yaml". It fails the test
// when rel is empty, absolute or has "." or ".." elements, so a fixture path
// always names one place inside the repository.
func Path(tb testing.TB, rel string) string {
	tb.Helper()
	if !fs.ValidPath(rel) {
		tb.Fatalf("repopath: %q is not a slash-separated path inside the repository root", rel)
	}
	return filepath.Join(Root(tb), filepath.FromSlash(rel))
}

// Read returns the contents of the file at rel, a root-relative path as Path
// takes it. It fails the test when the file cannot be read.
func Read(tb testing.TB, rel string) []byte {
	tb.Helper()
	data, err := os.ReadFile(Path(tb, rel))
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

// SkipDir is the walk rule the repository-wide walks in tests share. It
// reports whether a walk from root should skip the directory at path: one
// whose name starts with ".", testdata, vendor, node_modules, or any
// directory below root that holds its own go.mod. It never skips root itself
// or an entry that is not a directory. Use it from a filepath.WalkDir
// callback by returning filepath.SkipDir when it reports true.
func SkipDir(root, path string, d fs.DirEntry) bool {
	if !d.IsDir() || filepath.Clean(path) == filepath.Clean(root) {
		return false
	}
	switch name := d.Name(); {
	case strings.HasPrefix(name, "."), name == "testdata", name == "vendor", name == "node_modules":
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// GoDirs returns the directories under root that hold .go files directly, as
// sorted, slash-separated paths relative to root ("." for root itself),
// walking with SkipDir. A directory of _test.go files alone counts, since it
// is still a package directory.
func GoDirs(root string) ([]string, error) {
	dirs := map[string]bool{}
	err := fs.WalkDir(os.DirFS(root), ".", func(rel string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && SkipDir(root, filepath.Join(root, filepath.FromSlash(rel)), d):
			return fs.SkipDir
		case !d.IsDir() && strings.HasSuffix(rel, ".go"):
			dirs[path.Dir(rel)] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the Go directories under %s: %w", root, err)
	}
	return slices.Sorted(maps.Keys(dirs)), nil
}
