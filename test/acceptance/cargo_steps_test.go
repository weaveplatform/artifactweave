package acceptance

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerCargo(sc *godog.ScenarioContext) {
	sc.Step(`^a Cargo project and consumer for registry "([^"]*)"$`, w.cargoProjects)
	sc.Step(`^I run cargo "([^"]*)"$`, w.runCargo)
	sc.Step(`^I clear Cargo's registry cache$`, w.clearCargoCache)
	sc.Step(`^a Cargo upstream serves the packaged crate$`, w.cargoUpstream)
}

func (w *world) cargoProjects(repo string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_CARGO_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	config := fmt.Sprintf(
		"[registry]\nglobal-credential-providers = [\"cargo:token\"]\n[registries.weave]\nindex = \"sparse+%s/artifactory/api/cargo/%s/index/\"\n",
		base,
		repo,
	)
	for name, files := range map[string]map[string]string{
		".cargo":         {"config.toml": config},
		"cargo-project":  {"Cargo.toml": "[package]\nname = \"hello_cargo\"\nversion = \"1.0.0\"\nedition = \"2021\"\ndescription = \"controlled Cargo test crate\"\nlicense = \"MIT\"\npublish = [\"weave\"]\n", "src/lib.rs": "pub fn answer() -> u32 { 42 }\n"},
		"cargo-consumer": {"Cargo.toml": "[package]\nname = \"cargo_consumer\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[dependencies]\nhello_cargo = { version = \"1.0.0\", registry = \"weave\" }\n", "src/main.rs": "fn main() { assert_eq!(hello_cargo::answer(), 42); }\n"},
	} {
		for path, content := range files {
			target := filepath.Join(dir, name, path)
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *world) runCargo(args string) error {
	token := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+testAdminPassword))
	flags := []string{
		"-e",
		"CARGO_HOME=/work/.cargo",
		"-e",
		"CARGO_REGISTRIES_WEAVE_TOKEN=" + token,
	}
	return w.runContainer(w.cargoImage, flags, append([]string{"cargo"}, w.clientArgs(args)...))
}

func (w *world) clearCargoCache() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"registry", "git"} {
		if err := os.RemoveAll(filepath.Join(dir, ".cargo", name)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, "cargo-consumer", "target")); err != nil {
		return err
	}
	return nil
}

func (w *world) cargoUpstream() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(
		filepath.Join(dir, "cargo-project", "target", "package", "hello_cargo-1.0.0.crate"),
	)
	if err != nil {
		return err
	}
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	record := map[string]any{
		"name":     "hello_cargo",
		"vers":     "1.0.0",
		"deps":     []any{},
		"cksum":    hex.EncodeToString(sum[:]),
		"features": map[string]any{},
		"yanked":   false,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	config, err := json.Marshal(map[string]string{"dl": w.remote.URL + "/crates"})
	if err != nil {
		return err
	}
	w.remote.Set("/config.json", fakeupstream.Resource{Body: config})
	w.remote.Set("/he/ll/hello_cargo", fakeupstream.Resource{Body: append(line, '\n')})
	w.remote.Set(
		"/crates/hello_cargo/1.0.0/download",
		fakeupstream.Resource{Redirect: "/mirror/hello_cargo-1.0.0.crate"},
	)
	w.remote.Set("/mirror/hello_cargo-1.0.0.crate", fakeupstream.Resource{Body: archive})
	return nil
}
