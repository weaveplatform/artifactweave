package app

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// TestAccConanRemoteClient runs pinned Conan clients through the production
// source/cache policy against a local Conan upstream made by the same client.
func TestAccConanRemoteClient(t *testing.T) {
	for _, tc := range []struct {
		name, env, recipe, reference     string
		create, upload, remove, download []string
	}{
		{"v2", "ARTIFACTWEAVE_CONAN2_BIN", "from conan import ConanFile\nfrom conan.tools.files import save\nimport os\nclass Hello(ConanFile):\n    name='hello'\n    version='1.0'\n    def package(self): save(self, os.path.join(self.package_folder,'hello.txt'),'hello')\n", "hello/1.0", []string{"create", "."}, []string{"upload", "hello/1.0", "-r", "origin", "-c"}, []string{"remove", "hello/1.0", "-c"}, []string{"download", "hello/1.0", "-r", "proxy"}},
		{"v1", "ARTIFACTWEAVE_CONAN1_BIN", "from conans import ConanFile\nimport os\nclass Hello(ConanFile):\n    name='hello'\n    version='1.0'\n    def package(self):\n        with open(os.path.join(self.package_folder,'hello.txt'),'w') as out: out.write('hello')\n", "hello/1.0@_/_", []string{"create", ".", "hello/1.0@_/_", "-pr", "./conan.profile"}, []string{"upload", "hello/1.0@_/_", "-r", "origin", "--all", "-c"}, []string{"remove", "hello/1.0@_/_", "-f"}, []string{"download", "hello/1.0@_/_", "-r", "proxy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := os.Getenv(tc.env)
			if bin == "" {
				t.Skip(tc.env + " is not set")
			}
			w := newRemoteWorld(t)
			srv := httptest.NewServer(w.h)
			defer srv.Close()
			origin := *repo.Defaults(&repo.Config{Key: "conan-origin", Class: repo.Local, PackageType: repo.Conan})
			proxy := *repo.Defaults(&repo.Config{Key: "conan-proxy", Class: repo.Remote, PackageType: repo.Conan, URL: srv.URL + "/artifactory/api/conan/conan-origin", Username: "admin", Password: "pw"})
			for _, c := range []repo.Config{origin, proxy} {
				if err := w.rt.store.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
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
			run("remote", "add", "origin", srv.URL+"/artifactory/api/conan/conan-origin")
			run("remote", "add", "proxy", srv.URL+"/artifactory/api/conan/conan-proxy")
			if tc.name == "v2" {
				run("remote", "login", "origin", "admin", "-p", "pw")
				run("remote", "login", "proxy", "admin", "-p", "pw")
				run("profile", "detect", "--force")
			} else {
				run("user", "admin", "-p", "pw", "-r", "origin")
				run("user", "admin", "-p", "pw", "-r", "proxy")
			}
			run(tc.create...)
			run(tc.upload...)
			run(tc.remove...)
			run(tc.download...)
		})
	}
}
