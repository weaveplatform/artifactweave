package server

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// TestAccConanClients exercises the native HTTP API with both supported Conan
// client generations. CI supplies exact, pinned binaries; a developer may
// run the same test with two isolated Python environments.
func TestAccConanClients(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		recipe    string
		commands  [][]string
	}{
		{"v2", "ARTIFACTWEAVE_CONAN2_BIN", "from conan import ConanFile\nfrom conan.tools.files import save\nimport os\nclass Hello(ConanFile):\n    name = 'hello'\n    version = '1.0'\n    def package(self):\n        save(self, os.path.join(self.package_folder, 'hello.txt'), 'hello')\n", [][]string{{"profile", "detect", "--force"}, {"create", "."}, {"upload", "hello/1.0", "-r", "aw", "-c"}, {"remove", "hello/1.0", "-c"}, {"download", "hello/1.0", "-r", "aw"}}},
		{"v1", "ARTIFACTWEAVE_CONAN1_BIN", "from conans import ConanFile\nimport os\nclass Hello(ConanFile):\n    name = 'hello'\n    version = '1.0'\n    def package(self):\n        with open(os.path.join(self.package_folder, 'hello.txt'), 'w') as out: out.write('hello')\n", [][]string{{"create", ".", "hello/1.0@_/_", "-pr", "./conan.profile"}, {"upload", "hello/1.0@_/_", "-r", "aw", "--all", "-c"}, {"remove", "hello/1.0@_/_", "-f"}, {"download", "hello/1.0@_/_", "-r", "aw"}}},
	} {
		for _, repoKey := range []string{"conan-local", "conan-fed", "conan-virtual"} {
			t.Run(tc.name+"/"+repoKey, func(t *testing.T) {
				bin := os.Getenv(tc.env)
				if bin == "" {
					t.Skip(tc.env + " is not set")
				}
				w := newItemWorld(t, false, func(cfg *Config) {
					cfg.Tokens, _ = seedTokens(t, cfg.Store)
					var err error
					cfg.Auth, err = auth.NewAuthenticator(
						cfg.Store.Users(),
						cfg.Store.Settings(),
						auth.Options{Hash: testHash, Tokens: cfg.Tokens},
					)
					if err != nil {
						t.Fatal(err)
					}
				})
				for _, c := range []repo.Config{
					{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
					{Key: "conan-fed", Class: repo.Federated, PackageType: repo.Conan},
					{Key: "conan-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-local"}, DefaultDeploymentRepo: "conan-local"},
				} {
					if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
						t.Fatal(err)
					}
				}
				srv := httptest.NewServer(w.h)
				defer srv.Close()
				work := t.TempDir()
				if err := os.WriteFile(
					filepath.Join(work, "conanfile.py"),
					[]byte(tc.recipe),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					filepath.Join(work, "conan.profile"),
					[]byte("[settings]\nos=Linux\narch=x86_64\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				home := filepath.Join(work, "home")
				if err := os.MkdirAll(home, 0o700); err != nil {
					t.Fatal(err)
				}
				env := append(
					os.Environ(),
					"CONAN_HOME="+home,
					"CONAN_USER_HOME="+home,
					"CONAN_REVISIONS_ENABLED=1",
				)
				run := func(args ...string) {
					t.Helper()
					cmd := exec.CommandContext(t.Context(), bin, args...)
					cmd.Dir = work
					cmd.Env = env
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("conan %s: %v\n%s", strings.Join(args, " "), err, out)
					}
				}
				run("remote", "add", "aw", srv.URL+"/artifactory/api/conan/"+repoKey)
				if tc.name == "v2" {
					run("remote", "login", "aw", "admin", "-p", "pw")
				} else {
					run("user", "admin", "-p", "pw", "-r", "aw")
				}
				for _, cmd := range tc.commands {
					run(cmd...)
				}
			})
		}
	}
}
