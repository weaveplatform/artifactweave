package clienttest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// SeedMavenTools copies only checksum-verified pinned tools into a private repository or JF home.
func SeedMavenTools(root string, args ...string) error {
	cache := os.Getenv("ARTIFACTWEAVE_MAVEN_TOOLS")
	if cache == "" {
		return errors.New("maven tools are not prepared: run test/acceptance/clients/maven-tools.py prepare and set ARTIFACTWEAVE_MAVEN_TOOLS")
	}
	argv := append([]string{
		filepath.Join(root, "test", "acceptance", "clients", "maven-tools.py"),
		"seed", "--cache", cache,
	}, args...)
	if out, err := exec.Command("python3", argv...).CombinedOutput(); err != nil {
		return fmt.Errorf("seed Maven tools: %w: %s", err, out)
	}
	return nil
}

// MavenToolLock is the set of paths the committed manifest allows in a tool
// repository; a read error yields nil, which every check then rejects.
func MavenToolLock(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, "test", "acceptance", "clients", "maven-tools.lock.json"))
	if err != nil {
		return nil
	}
	var lock struct {
		Repository map[string]string `json:"repository"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return nil
	}
	names := make([]string, 0, len(lock.Repository))
	for name := range lock.Repository {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// CheckMavenToolRepository runs before every client invocation, including a
// later cold head in the same scenario. The tail must hold exactly the
// manifest's tools: a product file there would answer a resolution that the
// scenario asserts came from the registry, and a deny list of namespaces
// would miss a new fixture group.
func CheckMavenToolRepository(repository string, allowed []string) error {
	if len(allowed) == 0 {
		return errors.New("maven tool manifest is unreadable or empty")
	}
	var found []string
	err := filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(name))
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect Maven tool repository: %w", err)
	}
	slices.Sort(found)
	for _, name := range found {
		if _, ok := slices.BinarySearch(allowed, name); !ok {
			return fmt.Errorf("maven tool repository holds %s, which is not a pinned tool", name)
		}
	}
	if len(found) != len(allowed) {
		return fmt.Errorf("maven tool repository holds %d of %d pinned tools", len(found), len(allowed))
	}
	return nil
}
