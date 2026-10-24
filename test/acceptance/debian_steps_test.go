package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

func (w *world) registerDebian(sc *godog.ScenarioContext) {
	sc.Step(`^I create a Debian signing pair "([^"]*)"$`, w.createDebianSigningPair)
	sc.Step(`^a dpkg-built Debian package "([^"]*)" at "([^"]*)" under "([^"]*)"$`,
		w.copyDebianPackage)
	sc.Step(`^the apt repository is "([^"]*)"$`, w.setAptRepository)
	sc.Step(`^I run apt "([^"]*)"$`, w.runApt)
}

func (w *world) createDebianSigningPair(name string) error {
	key, err := terraformtest.NewKey()
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "debian.asc"), []byte(key.Public), 0o644); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"pairName": name, "alias": name, "pairType": "GPG",
		"publicKey": key.Public, "privateKey": key.Private,
	})
	if err != nil {
		return err
	}
	return w.send(http.MethodPost, "/artifactory/api/security/keypair", string(body),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

func (w *world) copyDebianPackage(name, version, folder string) error {
	if strings.ContainsAny(name+version+folder, "/\\") {
		return fmt.Errorf("unsafe Debian fixture coordinates")
	}
	content, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "debian",
		"testdata", name+"_"+version+"_amd64_0.deb"))
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, folder, name+"_"+version+"_amd64.deb"),
		content, 0o600)
}

func (w *world) setAptRepository(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_DEBIAN_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	sources := "deb [signed-by=/work/debian.asc] " + base + "/artifactory/" + key +
		" stable main\n"
	if err := os.WriteFile(filepath.Join(dir, "sources.list"), []byte(sources), 0o644); err != nil {
		return err
	}
	auth := "machine " + u.Scheme + "://" + u.Host +
		" login admin password " + testAdminPassword + "\n"
	if err := os.WriteFile(filepath.Join(dir, "auth.conf"), []byte(auth), 0o600); err != nil {
		return err
	}
	for _, path := range []string{"lists/partial", "archives/partial"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) runApt(args string) error {
	command := []string{
		"apt-get",
		"-o", "Dir::Etc::sourcelist=/work/sources.list",
		"-o", "Dir::Etc::sourceparts=-",
		"-o", "Dir::Etc::netrc=/work/auth.conf",
		"-o", "Dir::State::lists=/work/lists",
		"-o", "Dir::Cache::archives=/work/archives",
		"-o", "Acquire::Languages=none",
	}
	command = append(command, strings.Fields(w.expand(args))...)
	return w.runContainer(w.debianImage, []string{
		"--platform", "linux/amd64", "--user", "0:0", "--entrypoint", "apt-get",
		"-e", "DEBIAN_FRONTEND=noninteractive",
	}, command[1:])
}
