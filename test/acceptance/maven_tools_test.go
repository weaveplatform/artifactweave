package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/test/clienttest"
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

func seedMavenTools(args ...string) error { return clienttest.SeedMavenTools(repoRoot, args...) }
func mavenToolLock() []string             { return clienttest.MavenToolLock(repoRoot) }
func checkMavenToolRepository(repository string, allowed []string) error {
	return clienttest.CheckMavenToolRepository(repository, allowed)
}

func TestUnitMavenToolsRecheckAnExistingTail(t *testing.T) {
	w := &world{mavenTools: t.TempDir()}
	if _, err := w.mavenToolRepository(); err == nil {
		t.Fatal("a later Maven command reused a tool tail holding a product file")
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
	return clienttest.MavenArgs(w.clientArgs(strings.ReplaceAll(args, "{m2}", "/m2")))
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
