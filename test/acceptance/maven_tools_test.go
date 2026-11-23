package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mavenToolRepository gives each scenario a verified, private copy of only the
// pinned build tools. It is never a developer home or a mutable CI cache.
func (w *world) mavenToolRepository() (string, error) {
	if w.mavenTools != "" {
		return w.mavenTools, checkMavenToolRepository(w.mavenTools, mavenToolLock())
	}
	dir, err := w.filesDir()
	if err != nil {
		return "", err
	}
	repository := filepath.Join(dir, "maven-tools")
	if err = seedMavenTools("--repository", repository); err != nil {
		return "", err
	}
	if err = checkMavenToolRepository(repository, mavenToolLock()); err != nil {
		return "", err
	}
	settings, err := os.ReadFile(filepath.Join(repoRoot, "test", "acceptance", "clients", "maven-tools-settings.xml"))
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(dir, "maven-tools-settings.xml"), settings, 0o600); err != nil {
		return "", err
	}
	w.mavenTools = repository
	return repository, nil
}

// seedMavenExtractor puts the pinned JF Maven extractor in the scenario's JF
// home, independent of when the scenario first ran plain Maven. Without it the
// CLI downloads an extractor from releases.jfrog.io during the assertion.
func (w *world) seedMavenExtractor() error {
	if w.mavenExtractorHome == w.jfHome {
		return nil
	}
	if err := seedMavenTools("--jf-home", w.jfHome); err != nil {
		return err
	}
	w.mavenExtractorHome = w.jfHome
	return nil
}

func seedMavenTools(args ...string) error {
	cache := os.Getenv("ARTIFACTWEAVE_MAVEN_TOOLS")
	if cache == "" {
		return errors.New("maven tools are not prepared: run test/acceptance/clients/maven-tools.py prepare and set ARTIFACTWEAVE_MAVEN_TOOLS")
	}
	argv := append([]string{
		filepath.Join(repoRoot, "test", "acceptance", "clients", "maven-tools.py"),
		"seed", "--cache", cache,
	}, args...)
	if out, err := exec.Command("python3", argv...).CombinedOutput(); err != nil {
		return fmt.Errorf("seed Maven tools: %w: %s", err, out)
	}
	return nil
}

// mavenToolLock is the set of paths the committed manifest allows in a tool
// repository; a read error yields nil, which every check then rejects.
func mavenToolLock() []string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "test", "acceptance", "clients", "maven-tools.lock.json"))
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

// checkMavenToolRepository runs before every client invocation, including a
// later cold head in the same scenario. The tail must hold exactly the
// manifest's tools: a product file there would answer a resolution that the
// scenario asserts came from the registry, and a deny list of namespaces
// would miss a new fixture group.
func checkMavenToolRepository(repository string, allowed []string) error {
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
	if err := checkMavenToolRepository(seed(t, allowed...), allowed); err != nil {
		t.Fatal(err)
	}
	for name, names := range map[string][]string{
		"product jar":      append(slices.Clone(allowed), "org/acme/smoke/lib/1.0.0/lib-1.0.0.jar"),
		"new fixture":      append(slices.Clone(allowed), "com/example/app/1/app-1.pom"),
		"resolver records": append(slices.Clone(allowed), "org/apache/tool/1/_remote.repositories"),
		"incomplete":       allowed[:1],
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkMavenToolRepository(seed(t, names...), allowed); err == nil {
				t.Fatal("client accepted a tool tail that is not exactly the pinned tools")
			}
		})
	}
	if err := checkMavenToolRepository(seed(t, allowed...), nil); err == nil {
		t.Fatal("an unreadable manifest allowed a tool tail")
	}
	w := &world{mavenTools: seed(t, "org/acme/smoke/lib/1.0.0/lib-1.0.0.jar")}
	if _, err := w.mavenToolRepository(); err == nil {
		t.Fatal("a later Maven command reused a tool tail holding a product file")
	}
}

func TestUnitMavenCommittedManifestNamesTools(t *testing.T) {
	names := mavenToolLock()
	if len(names) == 0 {
		t.Fatal("committed Maven tool manifest is unreadable or empty")
	}
	for _, name := range names {
		if strings.HasPrefix(name, "org/acme/") || strings.HasPrefix(name, "org/weave/") {
			t.Errorf("manifest pins product coordinate %s", name)
		}
	}
}

func TestUnitMavenToolsRequirePreparation(t *testing.T) {
	t.Setenv("ARTIFACTWEAVE_MAVEN_TOOLS", "")
	w := &world{dir: t.TempDir()}
	if _, err := w.mavenToolRepository(); err == nil {
		t.Fatal("Maven ran without a prepared tool supply")
	}
	w.jfHome = t.TempDir()
	if err := w.seedMavenExtractor(); err == nil {
		t.Fatal("jf mvn ran without the pinned extractor")
	}
}

func (w *world) mavenClientArgs(args string) []string {
	argv := w.clientArgs(strings.ReplaceAll(args, "{m2}", "/m2"))
	for i, arg := range argv {
		if arg == "-Dmaven.repo.local=/m2" {
			// deploy writes remote metadata even without install. Keep that
			// product state in a head, never in the read-only tool supply.
			argv[i] = "-Dmaven.repo.local=/work/maven-publisher"
		}
	}
	return append([]string{
		"-B", "--no-transfer-progress", "--global-settings", "/work/maven-tools-settings.xml",
		"-Dmaven.repo.local=/work/maven-publisher", "-Dmaven.repo.local.tail=/m2",
	}, argv...)
}

func TestUnitMavenProductHeadsStaySeparateFromTools(t *testing.T) {
	for _, test := range []struct {
		args string
		head string
	}{
		{"package deploy:deploy -Dmaven.repo.local={m2}", "/work/maven-publisher"},
		{"package -Dmaven.repo.local=/work/fresh -Dmaven.repo.local.tail={m2}", "/work/fresh"},
		{"package -Dmaven.repo.local=/work/disabled-product -Dmaven.repo.local.tail={m2}", "/work/disabled-product"},
	} {
		w := &world{}
		properties := map[string]string{}
		for _, arg := range w.mavenClientArgs(test.args) {
			if key, value, found := strings.Cut(arg, "="); found {
				properties[key] = value
			}
		}
		if got := properties["-Dmaven.repo.local"]; got != test.head {
			t.Errorf("%s: product head %q, want %q", test.args, got, test.head)
		}
		if got := properties["-Dmaven.repo.local.tail"]; got != "/m2" {
			t.Errorf("%s: tool tail %q, want /m2", test.args, got)
		}
	}
}
